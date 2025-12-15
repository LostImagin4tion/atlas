// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

//go:build !ent

package ydb

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ariga.io/atlas/sql/schema"
)

// FormatType converts a schema.Type to its YDB string representation.
func FormatType(t schema.Type) (string, error) {
	var (
		f   string
		err error
	)

	switch t := t.(type) {
	case *schema.BoolType:
		f = TypeBool
	case *schema.IntegerType:
		f, err = formatIntegerType(t)
	case *schema.FloatType:
		f, err = formatFloatType(t)
	case *schema.DecimalType:
		f, err = formatDecimalType(t)
	case *SerialType:
		f = t.T
	case *schema.BinaryType:
		f = TypeString
	case *schema.StringType:
		f = TypeUtf8
	case *schema.JSONType:
		f, err = formatJSONType(t)
	case YsonType:
		f = t.T
	case *schema.UUIDType:
		f = TypeUuid
	case *schema.TimeType:
		f, err = formatTimeType(t)
	case *schema.UnsupportedType:
		err = fmt.Errorf("ydb: unsupported type: %q", t.T)
	default:
		err = fmt.Errorf("ydb: invalid schema type: %T", t)
	}

	if err != nil {
		return "", err
	}
	return f, nil
}

func formatIntegerType(t *schema.IntegerType) (string, error) {
	typ := strings.ToLower(t.T)
	switch typ {
	case TypeInt8:
		if t.Unsigned {
			return TypeUint8, nil
		}
		return TypeInt8, nil
	case TypeInt16:
		if t.Unsigned {
			return TypeUint16, nil
		}
		return TypeInt16, nil
	case TypeInt32:
		if t.Unsigned {
			return TypeUint32, nil
		}
		return TypeInt32, nil
	case TypeInt64:
		if t.Unsigned {
			return TypeUint64, nil
		}
		return TypeInt64, nil
	case TypeUint8, TypeUint16, TypeUint32, TypeUint64:
		return typ, nil
	default:
		return "", fmt.Errorf("ydb: unsupported object identifier type: %q", t.T)
	}
}

func formatFloatType(t *schema.FloatType) (string, error) {
	typ := strings.ToLower(t.T)
	switch typ {
	case TypeFloat, TypeDouble:
		return typ, nil
	default:
		return "", fmt.Errorf("ydb: unsupported object identifier type: %q", t.T)
	}
}

func formatDecimalType(t *schema.DecimalType) (string, error) {
	if t.Precision > 0 {
		if t.Scale > 0 {
			return fmt.Sprintf("%s(%d,%d)", TypeDecimal, t.Precision, t.Scale), nil
		}
		return fmt.Sprintf("%s(%d,%d)", TypeDecimal, t.Precision, t.Precision), nil
	}
	return fmt.Sprintf("%s(22,9)", TypeDecimal), nil
}

func formatJSONType(t *schema.JSONType) (string, error) {
	typ := strings.ToLower(t.T)
	switch typ {
	case TypeJsonDocument, TypeJson:
		return typ, nil
	default:
		return "", fmt.Errorf("ydb: unsupported object identifier type: %q", t.T)
	}
}

func formatTimeType(t *schema.TimeType) (string, error) {
	switch typ := strings.ToLower(t.T); typ {
	case TypeDate,
		TypeDate32,
		TypeDateTime,
		TypeDateTime64,
		TypeTimestamp,
		TypeTimestamp64,
		TypeInterval,
		TypeInterval64,
		TypeTzDate,
		TypeTzDate32,
		TypeTzDateTime,
		TypeTzDateTime64,
		TypeTzTimestamp,
		TypeTzTimestamp64:
		return typ, nil
	default:
		return "", fmt.Errorf("ydb: unsupported object identifier type: %q", t.T)
	}
}

// ParseType returns the schema.Type value represented by the given raw type.
// The raw value is expected to follow the format of input for the CREATE TABLE statement.
func ParseType(typ string) (schema.Type, error) {
	colDesc, err := parseColumn(typ)
	if err != nil {
		return nil, err
	}

	return columnType(colDesc), nil
}

type columnDecscriptor struct {
	strT      string
	precision int64
	scale     int64
	parts     []string
}

func parseColumn(typ string) (*columnDecscriptor, error) {
	if len(typ) == 0 {
		return nil, errors.New("ydb: unexpected empty column type")
	}
	parts := strings.FieldsFunc(typ, func(r rune) bool {
		return r == '(' || r == ')' || r == ' ' || r == ','
	})

	var (
		err     error
		colDesc = &columnDecscriptor{
			strT:  strings.ToLower(parts[0]),
			parts: parts,
		}
	)

	switch colDesc.strT {
	case TypeDecimal:
		err = parseDecimalType(parts, colDesc)
	case TypeFloat:
		colDesc.precision = 24
	case TypeDouble:
		colDesc.precision = 53
	}

	if err != nil {
		return nil, err
	}
	return colDesc, nil
}

func parseDecimalType(parts []string, colDesc *columnDecscriptor) error {
	if len(parts) < 3 {
		return errors.New("ydb: decimal should specify precision and scale")
	}

	precision, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || precision < 1 || precision > 35 {
		return fmt.Errorf("ydb: invalid decimal precision: %q", parts[1])
	}

	scale, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || scale < 0 || scale > precision {
		return fmt.Errorf("ydb: invalid decimal scale: %q", parts[1])
	}

	colDesc.precision = precision
	colDesc.scale = scale

	return nil
}

func columnType(colDesc *columnDecscriptor) schema.Type {
	var typ schema.Type

	switch strT := colDesc.strT; strT {
	case TypeBool:
		typ = &schema.BoolType{T: strT}
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64:
		typ = &schema.IntegerType{T: strT, Unsigned: false}
	case TypeUint8, TypeUint16, TypeUint32, TypeUint64:
		typ = &schema.IntegerType{T: strT, Unsigned: true}
	case TypeFloat, TypeDouble:
		typ = &schema.FloatType{T: strT, Precision: int(colDesc.precision)}
	case TypeDecimal:
		typ = &schema.DecimalType{T: strT, Precision: int(colDesc.precision), Scale: int(colDesc.scale)}
	case TypeSmallSerial, TypeSerial2, TypeSerial, TypeSerial4, TypeSerial8, TypeBigSerial:
		typ = &SerialType{T: strT}
	case TypeString:
		typ = &schema.BinaryType{T: strT}
	case TypeUtf8:
		typ = &schema.StringType{T: strT}
	case TypeJson, TypeJsonDocument:
		typ = &schema.JSONType{T: strT}
	case TypeYson:
		typ = &YsonType{T: strT}
	case TypeUuid:
		typ = &schema.UUIDType{T: strT}
	case TypeDate,
		TypeDate32,
		TypeDateTime,
		TypeDateTime64,
		TypeTimestamp,
		TypeTimestamp64,
		TypeInterval,
		TypeInterval64,
		TypeTzDate,
		TypeTzDate32,
		TypeTzDateTime,
		TypeTzDateTime64,
		TypeTzTimestamp,
		TypeTzTimestamp64:
		typ = &schema.TimeType{T: strT}
	default:
		typ = &schema.UnsupportedType{T: strT}
	}

	return typ
}
