// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

//go:build !ent

package ydb

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"ariga.io/atlas/sql/internal/sqlx"
	"ariga.io/atlas/sql/schema"
	"github.com/ydb-platform/ydb-go-sdk/v3/scheme"
)

// inspect provides a YDB implementation for schema.Inspector.
type inspect struct {
	*conn
}

var _ schema.Inspector = (*inspect)(nil)

// InspectRealm returns schema descriptions of all resources in the given realm.
func (i *inspect) InspectRealm(ctx context.Context, opts *schema.InspectRealmOption) (*schema.Realm, error) {
	schemas, err := i.schemas(ctx, opts)
	if err != nil {
		return nil, err
	}

	if opts == nil {
		opts = &schema.InspectRealmOption{}
	}

	r := schema.NewRealm(schemas...)
	mode := sqlx.ModeInspectRealm(opts)

	if len(schemas) > 0 && mode.Is(schema.InspectTables) {
		if err := i.inspectTables(ctx, r, nil); err != nil {
			return nil, err
		}
	}
	return schema.ExcludeRealm(r, opts.Exclude)
}

// InspectSchema returns schema descriptions of the tables in the given schema.
// If the schema name is empty, the result will be the connected database.
func (i *inspect) InspectSchema(ctx context.Context, name string, opts *schema.InspectOptions) (*schema.Schema, error) {
	if name == "" && i.database != "" {
		name = i.database
	}

	schemas, err := i.schemas(ctx, &schema.InspectRealmOption{Schemas: []string{name}})
	if err != nil {
		return nil, err
	}

	switch n := len(schemas); {
	case n == 0:
		if name == "" {
			return nil, &schema.NotExistError{Err: fmt.Errorf("ydb: no schema found")}
		}
		return nil, &schema.NotExistError{Err: fmt.Errorf("ydb: schema %q was not found", name)}
	case n > 1:
		return nil, fmt.Errorf("ydb: %d schemas were found for %q", n, name)
	}

	if opts == nil {
		opts = &schema.InspectOptions{}
	}

	r := schema.NewRealm(schemas...)
	mode := sqlx.ModeInspectSchema(opts)

	if mode.Is(schema.InspectTables) {
		if err := i.inspectTables(ctx, r, opts); err != nil {
			return nil, err
		}
	}

	return schema.ExcludeSchema(r.Schemas[0], opts.Exclude)
}

// schemas returns the list of schemas in the database.
func (i *inspect) schemas(ctx context.Context, opts *schema.InspectRealmOption) ([]*schema.Schema, error) {
	var names []string
	if opts != nil && len(opts.Schemas) > 0 {
		names = opts.Schemas
	} else if i.database != "" {
		names = []string{i.database}
	} else {
		return nil, errors.New("ydb: database path is not configured")
	}

	var schemas []*schema.Schema
	for _, name := range names {
		_, err := i.nativeDriver.Scheme().ListDirectory(ctx, name)
		if err != nil {
			return nil, &schema.NotExistError{
				Err: fmt.Errorf("ydb: path %q does not exist or is not accessible: %w", name, err),
			}
		}
		schemas = append(schemas, schema.New(name))
	}
	return schemas, nil
}

// inspectTables inspects all tables in the realm.
func (i *inspect) inspectTables(ctx context.Context, r *schema.Realm, opts *schema.InspectOptions) error {
	for _, s := range r.Schemas {
		if err := i.tables(ctx, s, opts); err != nil {
			return err
		}
		for _, t := range s.Tables {
			if err := i.columns(ctx, t); err != nil {
				return err
			}
			if err := i.indexes(ctx, t); err != nil {
				return err
			}
		}
	}
	return nil
}

type entryWithPath struct {
	*scheme.Entry
	fullPath string
}

// tables queries and populates the tables in the schema.
func (i *inspect) tables(ctx context.Context, s *schema.Schema, opts *schema.InspectOptions) error {
	rootPath := s.Name
	dir, err := i.nativeDriver.Scheme().ListDirectory(ctx, rootPath)
	if err != nil {
		return fmt.Errorf("ydb: failed list directory: %v", err)
	}

	queue := make([]entryWithPath, 0, len(dir.Children))
	for _, child := range dir.Children {
		queue = append(queue, entryWithPath{
			Entry:    &child,
			fullPath: fmt.Sprintf("%s/%s", rootPath, child.Name),
		})
	}

	for len(queue) != 0 {
		currEntry := queue[0]
		queue = queue[1:]

		switch currEntry.Type {
		case scheme.EntryTable:
			shouldAdd := opts == nil || len(opts.Tables) == 0 || slices.Contains(opts.Tables, currEntry.fullPath)

			if shouldAdd {
				t := schema.NewTable(currEntry.fullPath)
				s.AddTables(t)
			}

		case scheme.EntryDirectory:
			dir, err = i.nativeDriver.Scheme().ListDirectory(ctx, currEntry.fullPath)
			if err != nil {
				return fmt.Errorf("ydb: failed list directory: %v", err)
			}

			for _, child := range dir.Children {
				queue = append(queue, entryWithPath{
					Entry:    &child,
					fullPath: fmt.Sprintf("%s/%s", currEntry.fullPath, child.Name),
				})
			}
		}
	}

	return nil
}

// columns queries and populates the columns for the given table.
func (i *inspect) columns(ctx context.Context, t *schema.Table) error {
	desc, err := i.nativeDriver.Table().DescribeTable(ctx, t.Name)
	if err != nil {
		return fmt.Errorf("ydb: failed describe table: %v", err)
	}

	for _, column := range desc.Columns {
		dataType := column.Type.String()
		columnType, err := ParseType(dataType)
		if err != nil {
			columnType = &schema.UnsupportedType{T: dataType}
		}

		_, nullable := columnType.(OptionalType)

		c := &schema.Column{
			Name: column.Name,
			Type: &schema.ColumnType{
				Type: columnType,
				Raw:  dataType,
				Null: nullable,
			},
		}

		// TODO
		// if defaultVal.Valid {
		// 	c.Default = &schema.RawExpr{X: defaultVal.String}
		// }

		t.AddColumns(c)
	}

	return nil
}

// indexes queries and populates the indexes for the given table.
func (i *inspect) indexes(ctx context.Context, t *schema.Table) error {
	desc, err := i.nativeDriver.Table().DescribeTable(ctx, t.Name)
	if err != nil {
		return fmt.Errorf("ydb: failed describe table: %v", err)
	}

	var pkParts []*schema.IndexPart
	for i, keyColumn := range desc.PrimaryKey {
		column, ok := t.Column(keyColumn)
		if !ok {
			return fmt.Errorf("ydb: primary key column %q not found in table %q", keyColumn, t.Name)
		}

		pkParts = append(pkParts, &schema.IndexPart{
			SeqNo: i + 1,
			C:     column,
		})
	}
	if len(pkParts) > 0 {
		pk := &schema.Index{
			Name:   "PRIMARY",
			Unique: true,
			Table:  t,
			Parts:  pkParts,
		}
		t.SetPrimaryKey(pk)
	}

	for _, idx := range desc.Indexes {
		atlasIdx := &schema.Index{
			Name:  idx.Name,
			Table: t,
		}

		for _, columnName := range idx.IndexColumns {
			column, ok := t.Column(columnName)
			if !ok {
				return fmt.Errorf("ydb: index column %q not found in table %q", columnName, t.Name)
			}
			atlasIdx.Parts = append(atlasIdx.Parts, &schema.IndexPart{
				SeqNo: len(atlasIdx.Parts) + 1,
				C:     column,
			})
		}

		t.AddIndexes(atlasIdx)
	}

	return nil
}
