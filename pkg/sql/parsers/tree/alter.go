// Copyright 2021 Matrix Origin
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

package tree

import "fmt"

type AlterUser struct {
	statementImpl
	IfExists bool
	Users    []*User
	Role     *Role
	MiscOpt  UserMiscOption
	// comment or attribute
	CommentOrAttribute AccountCommentOrAttribute
}

func (node *AlterUser) Format(ctx *FmtCtx) {
	ctx.WriteString("alter user")
	if node.IfExists {
		ctx.WriteString(" if exists")
	}
	if node.Users != nil {
		prefix := " "
		for _, u := range node.Users {
			ctx.WriteString(prefix)
			u.Format(ctx)
			prefix = ", "
		}
	}
	if node.Role != nil {
		ctx.WriteString(" default role ")
		node.Role.Format(ctx)
	}
	if node.MiscOpt != nil {
		prefix := " "
		ctx.WriteString(prefix)
		node.MiscOpt.Format(ctx)
	}
	node.CommentOrAttribute.Format(ctx)
}

func (node *AlterUser) GetStatementType() string { return "Alter User" }
func (node *AlterUser) GetQueryType() string     { return QueryTypeDCL }

func NewAlterUser(ife bool, u []*User, r *Role, m UserMiscOption) *AlterUser {
	return &AlterUser{
		IfExists: ife,
		Users:    u,
		Role:     r,
		MiscOpt:  m,
	}
}

type AlterAccountAuthOption struct {
	Exist          bool
	Equal          string
	AdminName      string
	IdentifiedType AccountIdentified
}

func (node *AlterAccountAuthOption) Format(ctx *FmtCtx) {
	if node.Exist {
		ctx.WriteString(" admin_name")
		if len(node.Equal) != 0 {
			ctx.WriteString(" ")
			ctx.WriteString(node.Equal)
		}

		ctx.WriteString(fmt.Sprintf(" '%s'", node.AdminName))
		node.IdentifiedType.Format(ctx)
	}
}

type AlterAccount struct {
	statementImpl
	IfExists   bool
	Name       string
	AuthOption AlterAccountAuthOption
	//status_option or not
	StatusOption AccountStatus
	//comment or not
	Comment AccountComment
}

func (ca *AlterAccount) Format(ctx *FmtCtx) {
	ctx.WriteString("alter account ")
	if ca.IfExists {
		ctx.WriteString("if exists ")
	}
	ctx.WriteString(ca.Name)
	ca.AuthOption.Format(ctx)
	ca.StatusOption.Format(ctx)
	ca.Comment.Format(ctx)
}

func (ca *AlterAccount) GetStatementType() string { return "Alter Account" }
func (ca *AlterAccount) GetQueryType() string     { return QueryTypeDCL }

type AlterView struct {
	statementImpl
	IfExists bool
	Name     *TableName
	ColNames IdentifierList
	AsSource *Select
}

func (node *AlterView) Format(ctx *FmtCtx) {
	ctx.WriteString("alter ")

	ctx.WriteString("view ")

	if node.IfExists {
		ctx.WriteString("if exists ")
	}

	node.Name.Format(ctx)
	if len(node.ColNames) > 0 {
		ctx.WriteString(" (")
		node.ColNames.Format(ctx)
		ctx.WriteByte(')')
	}
	ctx.WriteString(" as ")
	node.AsSource.Format(ctx)
}

func (node *AlterView) GetStatementType() string { return "Alter View" }
func (node *AlterView) GetQueryType() string     { return QueryTypeDDL }

// alter configuration for mo_mysql_compatbility_mode
type AlterDataBaseConfig struct {
	statementImpl
	AccountName    string
	DbName         string
	IsAccountLevel bool
	UpdateConfig   string
}

func (node *AlterDataBaseConfig) Format(ctx *FmtCtx) {

	if node.IsAccountLevel {
		ctx.WriteString("alter ")
		ctx.WriteString("account configuration ")

		ctx.WriteString("for ")
		ctx.WriteString(fmt.Sprintf("%s ", node.AccountName))
	} else {
		ctx.WriteString("alter ")
		ctx.WriteString("database configuration ")

		ctx.WriteString("for ")
		ctx.WriteString(fmt.Sprintf("%s ", node.DbName))
	}

	ctx.WriteString("as ")
	ctx.WriteString(fmt.Sprintf("%s ", node.UpdateConfig))
}

func (node *AlterDataBaseConfig) GetStatementType() string { return "Alter DataBase config" }
func (node *AlterDataBaseConfig) GetQueryType() string     { return QueryTypeDDL }

// AlterTable
// see https://dev.mysql.com/doc/refman/8.0/en/alter-table.html
type AlterTable struct {
	statementImpl
	Table   TableName
	Options AlterTableOptions
}

func (node *AlterTable) Format(ctx *FmtCtx) {
	ctx.WriteString("alter table ")
	node.Table.Format(ctx)

	prefix := " "
	for _, t := range node.Options {
		ctx.WriteString(prefix)
		t.Format(ctx)
		prefix = ", "
	}
}

type AlterTableOptions = []AlterTableOption

type AlterTableOption interface {
	NodeFormatter
}

type alterOptionImpl struct {
	AlterTableOption
}

type AlterOptionAdd struct {
	alterOptionImpl
	Def TableDef
}

func (node *AlterOptionAdd) Format(ctx *FmtCtx) {
	ctx.WriteString("add ")
	node.Def.Format(ctx)
}

type AlterTableDropType int

const (
	AlterTableDropColumn AlterTableDropType = iota
	AlterTableDropIndex
	AlterTableDropKey
	AlterTableDropPrimaryKey
	AlterTableDropForeignKey
)

type AlterOptionDrop struct {
	alterOptionImpl
	Typ  AlterTableDropType
	Name Identifier
}

func (node *AlterOptionDrop) Format(ctx *FmtCtx) {
	ctx.WriteString("drop ")
	switch node.Typ {
	case AlterTableDropColumn:
		ctx.WriteString("column ")
		node.Name.Format(ctx)
	case AlterTableDropIndex:
		ctx.WriteString("index ")
		node.Name.Format(ctx)
	case AlterTableDropKey:
		ctx.WriteString("key ")
		node.Name.Format(ctx)
	case AlterTableDropPrimaryKey:
		ctx.WriteString("primary key")
	case AlterTableDropForeignKey:
		ctx.WriteString("foreign key ")
		node.Name.Format(ctx)
	}
}

type AccountsSetOption struct {
	All          bool
	SetAccounts  IdentifierList
	AddAccounts  IdentifierList
	DropAccounts IdentifierList
}

type AlterPublication struct {
	statementImpl
	IfExists    bool
	Name        Identifier
	AccountsSet *AccountsSetOption
	Comment     string
}

func (node *AlterPublication) Format(ctx *FmtCtx) {
	ctx.WriteString("alter publication ")
	if node.IfExists {
		ctx.WriteString("if exists ")
	}
	node.Name.Format(ctx)
	ctx.WriteString(" account ")
	if node.AccountsSet != nil {
		if node.AccountsSet.All {
			ctx.WriteString("all")
		} else {
			if len(node.AccountsSet.SetAccounts) > 0 {
				node.AccountsSet.SetAccounts.Format(ctx)
			}
			if len(node.AccountsSet.AddAccounts) > 0 {
				ctx.WriteString("add ")
				node.AccountsSet.AddAccounts.Format(ctx)
			}
			if len(node.AccountsSet.DropAccounts) > 0 {
				ctx.WriteString("drop ")
				node.AccountsSet.DropAccounts.Format(ctx)
			}
		}
	}
	if node.Comment != "" {
		ctx.WriteString(" comment ")
		ctx.WriteString(fmt.Sprintf("'%s'", node.Comment))
	}
}
