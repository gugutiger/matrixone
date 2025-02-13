// Copyright 2023 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package disttae

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"runtime/trace"
	"sync/atomic"

	"github.com/matrixorigin/matrixone/pkg/catalog"
	"github.com/matrixorigin/matrixone/pkg/container/batch"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/fileservice"
	"github.com/matrixorigin/matrixone/pkg/pb/api"
	"github.com/tidwall/btree"
)

var partitionStateProfileHandler = fileservice.NewProfileHandler()

func init() {
	http.Handle("/debug/cn-partition-state", partitionStateProfileHandler)
}

type PartitionState struct {
	// also modify the Copy method if adding fields
	Rows         *btree.BTreeG[RowEntry] // use value type to avoid locking on elements
	Blocks       *btree.BTreeG[BlockEntry]
	PrimaryIndex *btree.BTreeG[*PrimaryIndexEntry]
	Checkpoints  []string
}

// RowEntry represents a version of a row
type RowEntry struct {
	BlockID uint64 // we need to iter by block id, so put it first to allow faster iteration
	RowID   types.Rowid
	Time    types.TS

	ID                int64 // a unique version id, for primary index building and validating
	Deleted           bool
	Batch             *batch.Batch
	Offset            int64
	PrimaryIndexBytes []byte
}

func (r RowEntry) Less(than RowEntry) bool {
	// asc
	if r.BlockID < than.BlockID {
		return true
	}
	if than.BlockID < r.BlockID {
		return false
	}
	// asc
	if r.RowID.Less(than.RowID) {
		return true
	}
	if than.RowID.Less(r.RowID) {
		return false
	}
	// desc
	if than.Time.Less(r.Time) {
		return true
	}
	if r.Time.Less(than.Time) {
		return false
	}
	return false
}

type BlockEntry struct {
	catalog.BlockInfo

	CreateTime types.TS
	DeleteTime types.TS
}

func (b BlockEntry) Less(than BlockEntry) bool {
	if b.BlockID < than.BlockID {
		return true
	}
	if than.BlockID < b.BlockID {
		return false
	}
	return false
}

func (b *BlockEntry) Visible(ts types.TS) bool {
	return b.CreateTime.LessEq(ts) &&
		(b.DeleteTime.IsEmpty() || ts.Less(b.DeleteTime))
}

type PrimaryIndexEntry struct {
	Bytes      []byte
	RowEntryID int64

	// fields for validating
	BlockID uint64
	RowID   types.Rowid
}

func (p *PrimaryIndexEntry) Less(than *PrimaryIndexEntry) bool {
	if res := bytes.Compare(p.Bytes, than.Bytes); res < 0 {
		return true
	} else if res > 0 {
		return false
	}
	return p.RowEntryID < than.RowEntryID
}

func NewPartitionState() *PartitionState {
	opts := btree.Options{
		Degree: 64,
	}
	return &PartitionState{
		Rows:         btree.NewBTreeGOptions((RowEntry).Less, opts),
		Blocks:       btree.NewBTreeGOptions((BlockEntry).Less, opts),
		PrimaryIndex: btree.NewBTreeGOptions((*PrimaryIndexEntry).Less, opts),
	}
}

func (p *PartitionState) Copy() *PartitionState {
	checkpoints := make([]string, len(p.Checkpoints))
	copy(checkpoints, p.Checkpoints)
	return &PartitionState{
		Rows:         p.Rows.Copy(),
		Blocks:       p.Blocks.Copy(),
		PrimaryIndex: p.PrimaryIndex.Copy(),
		Checkpoints:  checkpoints,
	}
}

func (p *PartitionState) RowExists(rowID types.Rowid, ts types.TS) bool {
	iter := p.Rows.Iter()
	defer iter.Release()

	blockID := blockIDFromRowID(rowID)
	for ok := iter.Seek(RowEntry{
		BlockID: blockID,
		RowID:   rowID,
		Time:    ts,
	}); ok; ok = iter.Next() {
		entry := iter.Item()
		if entry.BlockID != blockID {
			break
		}
		if entry.RowID != rowID {
			break
		}
		if entry.Time.Greater(ts) {
			// not visible
			continue
		}
		if entry.Deleted {
			// deleted
			return false
		}
		return true
	}

	return false
}

func (p *PartitionState) HandleLogtailEntry(
	ctx context.Context,
	entry *api.Entry,
	primaryKeyIndex int,
	packer *types.Packer,
) {
	switch entry.EntryType {
	case api.Entry_Insert:
		if isMetaTable(entry.TableName) {
			p.HandleMetadataInsert(ctx, entry.Bat)
		} else {
			p.HandleRowsInsert(ctx, entry.Bat, primaryKeyIndex, packer)
		}
	case api.Entry_Delete:
		if isMetaTable(entry.TableName) {
			p.HandleMetadataDelete(ctx, entry.Bat)
		} else {
			p.HandleRowsDelete(ctx, entry.Bat)
		}
	default:
		panic("unknown entry type")
	}
}

var nextRowEntryID = int64(1)

func (p *PartitionState) HandleRowsInsert(
	ctx context.Context,
	input *api.Batch,
	primaryKeyIndex int,
	packer *types.Packer,
) {
	ctx, task := trace.NewTask(ctx, "PartitionState.HandleRowsInsert")
	defer task.End()

	rowIDVector := vector.MustFixedCol[types.Rowid](mustVectorFromProto(input.Vecs[0]))
	timeVector := vector.MustFixedCol[types.TS](mustVectorFromProto(input.Vecs[1]))
	batch, err := batch.ProtoBatchToBatch(input)
	if err != nil {
		panic(err)
	}
	var primaryKeyBytesSet [][]byte
	if primaryKeyIndex >= 0 {
		primaryKeyBytesSet = encodePrimaryKeyVector(
			batch.Vecs[2+primaryKeyIndex],
			packer,
		)
	}

	for i, rowID := range rowIDVector {
		trace.WithRegion(ctx, "handle a row", func() {

			blockID := blockIDFromRowID(rowID)
			pivot := RowEntry{
				BlockID: blockID,
				RowID:   rowID,
				Time:    timeVector[i],
			}
			entry, ok := p.Rows.Get(pivot)
			if !ok {
				entry = pivot
				entry.ID = atomic.AddInt64(&nextRowEntryID, 1)
			}

			entry.Batch = batch
			entry.Offset = int64(i)
			if i < len(primaryKeyBytesSet) {
				entry.PrimaryIndexBytes = primaryKeyBytesSet[i]
			}

			p.Rows.Set(entry)

			if i < len(primaryKeyBytesSet) && len(primaryKeyBytesSet[i]) > 0 {
				p.PrimaryIndex.Set(&PrimaryIndexEntry{
					Bytes:      primaryKeyBytesSet[i],
					RowEntryID: entry.ID,
					BlockID:    blockID,
					RowID:      rowID,
				})
			}

		})
	}

	partitionStateProfileHandler.AddSample()
}

func (p *PartitionState) HandleRowsDelete(ctx context.Context, input *api.Batch) {
	ctx, task := trace.NewTask(ctx, "PartitionState.HandleRowsDelete")
	defer task.End()

	rowIDVector := vector.MustFixedCol[types.Rowid](mustVectorFromProto(input.Vecs[0]))
	timeVector := vector.MustFixedCol[types.TS](mustVectorFromProto(input.Vecs[1]))
	batch, err := batch.ProtoBatchToBatch(input)
	if err != nil {
		panic(err)
	}

	for i, rowID := range rowIDVector {
		trace.WithRegion(ctx, "handle a row", func() {

			blockID := blockIDFromRowID(rowID)
			pivot := RowEntry{
				BlockID: blockID,
				RowID:   rowID,
				Time:    timeVector[i],
			}
			entry, ok := p.Rows.Get(pivot)
			if !ok {
				entry = pivot
				entry.ID = atomic.AddInt64(&nextRowEntryID, 1)
			}

			entry.Deleted = true
			entry.Batch = batch
			entry.Offset = int64(i)

			p.Rows.Set(entry)
		})
	}

	partitionStateProfileHandler.AddSample()
}

func (p *PartitionState) HandleMetadataInsert(ctx context.Context, input *api.Batch) {
	ctx, task := trace.NewTask(ctx, "PartitionState.HandleMetadataInsert")
	defer task.End()

	createTimeVector := vector.MustFixedCol[types.TS](mustVectorFromProto(input.Vecs[1]))
	blockIDVector := vector.MustFixedCol[uint64](mustVectorFromProto(input.Vecs[2]))
	entryStateVector := vector.MustFixedCol[bool](mustVectorFromProto(input.Vecs[3]))
	sortedStateVector := vector.MustFixedCol[bool](mustVectorFromProto(input.Vecs[4]))
	metaLocationVector := vector.MustStrCol(mustVectorFromProto(input.Vecs[5]))
	deltaLocationVector := vector.MustStrCol(mustVectorFromProto(input.Vecs[6]))
	commitTimeVector := vector.MustFixedCol[types.TS](mustVectorFromProto(input.Vecs[7]))
	segmentIDVector := vector.MustFixedCol[uint64](mustVectorFromProto(input.Vecs[8]))

	for i, blockID := range blockIDVector {
		trace.WithRegion(ctx, "handle a row", func() {

			pivot := BlockEntry{
				BlockInfo: catalog.BlockInfo{
					BlockID: blockID,
				},
			}
			entry, ok := p.Blocks.Get(pivot)
			if !ok {
				entry = pivot
			}

			if location := metaLocationVector[i]; location != "" {
				entry.MetaLoc = location
			}
			if location := deltaLocationVector[i]; location != "" {
				entry.DeltaLoc = location
			}
			if id := segmentIDVector[i]; id > 0 {
				entry.SegmentID = id
			}
			entry.Sorted = sortedStateVector[i]
			if t := createTimeVector[i]; !t.IsEmpty() {
				entry.CreateTime = t
			}
			if t := commitTimeVector[i]; !t.IsEmpty() {
				entry.CommitTs = t
			}
			entry.EntryState = entryStateVector[i]

			p.Blocks.Set(entry)

			if entryStateVector[i] {
				iter := p.Rows.Copy().Iter()
				pivot := RowEntry{
					BlockID: blockID,
				}
				for ok := iter.Seek(pivot); ok; ok = iter.Next() {
					entry := iter.Item()
					if entry.BlockID != blockID {
						break
					}
					// delete row entry
					p.Rows.Delete(entry)
					// delete primary index entry
					if len(entry.PrimaryIndexBytes) > 0 {
						p.PrimaryIndex.Delete(&PrimaryIndexEntry{
							Bytes:      entry.PrimaryIndexBytes,
							RowEntryID: entry.ID,
						})
					}
				}
				iter.Release()
			}

		})
	}

	partitionStateProfileHandler.AddSample()
}

func (p *PartitionState) HandleMetadataDelete(ctx context.Context, input *api.Batch) {
	ctx, task := trace.NewTask(ctx, "PartitionState.HandleMetadataDelete")
	defer task.End()

	rowIDVector := vector.MustFixedCol[types.Rowid](mustVectorFromProto(input.Vecs[0]))
	deleteTimeVector := vector.MustFixedCol[types.TS](mustVectorFromProto(input.Vecs[1]))

	for i, rowID := range rowIDVector {
		blockID := types.DecodeUint64(rowID[:8])
		trace.WithRegion(ctx, "handle a row", func() {

			pivot := BlockEntry{
				BlockInfo: catalog.BlockInfo{
					BlockID: blockID,
				},
			}
			entry, ok := p.Blocks.Get(pivot)
			if !ok {
				panic(fmt.Sprintf("invalid block id. %x", rowID))
			}

			entry.DeleteTime = deleteTimeVector[i]

			p.Blocks.Set(entry)
		})
	}

	partitionStateProfileHandler.AddSample()
}
