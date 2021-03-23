// Copyright 2021 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package typeinfo

import (
	"context"
	"fmt"
	"github.com/dolthub/dolt/go/libraries/doltcore/sqle/json"
	"github.com/dolthub/dolt/go/store/types"
	"github.com/dolthub/go-mysql-server/sql"
)

type jsonType struct {
	jsonType sql.JsonType
}

var _ TypeInfo = (*jsonType)(nil)
var JSONType = &jsonType{sql.JSON}

// ConvertNomsValueToValue implements TypeInfo interface.
func (ti *jsonType) ConvertNomsValueToValue(v types.Value) (interface{}, error) {
	if val, ok := v.(types.JSONDoc); ok {
		return json.NomsJSONValue(val), nil
	}
	if _, ok := v.(types.Null); ok || v == nil {
		return nil, nil
	}
	return nil, fmt.Errorf(`"%v" cannot convert NomsKind "%v" to a value`, ti.String(), v.Kind())
}

// ReadFrom reads a go value from a noms types.CodecReader directly
func (ti *jsonType) ReadFrom(_ *types.NomsBinFormat, reader types.CodecReader) (interface{}, error) {
	k := reader.PeekKind()
	switch k {
	case types.JSONDocKind:
		js, err := reader.ReadJSON()
		if err != nil {
			return nil, err
		}
		return json.NomsJSONValue(js), nil
	case types.NullKind:
		_ = reader.ReadKind()
		return nil, nil
	}

	return nil, fmt.Errorf(`"%v" cannot convert NomsKind "%v" to a value`, ti.String(), k)
}

// ConvertValueToNomsValue implements TypeInfo interface.
func (ti *jsonType) ConvertValueToNomsValue(ctx context.Context, vrw types.ValueReadWriter, v interface{}) (types.Value, error) {
	if v == nil {
		return types.NullValue, nil
	}

	jsDoc, err := ti.jsonType.Convert(v)
	if err != nil {
		return nil, err
	}

	jsVal, ok := jsDoc.(sql.JSONValue)
	if !ok {
		return nil, fmt.Errorf(`"%v" cannot convert value "%v" of type "%T" as it is invalid`, ti.String(), v, v)
	}

	noms, err := json.NomsJSONFromJSONValue(ctx, vrw, jsVal)
	if err != nil {
		return nil, err
	}

	return types.JSONDoc(noms), err
}

// Equals implements TypeInfo interface.
func (ti *jsonType) Equals(other TypeInfo) bool {
	return ti.GetTypeIdentifier() == other.GetTypeIdentifier()
}

// FormatValue implements TypeInfo interface.
func (ti *jsonType) FormatValue(v types.Value) (*string, error) {
	if noms, ok := v.(types.JSONDoc); ok {
		s, err := json.NomsJSONValue(noms).ToString()
		if err != nil {
			return nil, err
		}
		return &s, nil
	}

	s := v.HumanReadableString()
	return &s, nil
}

// GetTypeIdentifier implements TypeInfo interface.
func (ti *jsonType) GetTypeIdentifier() Identifier {
	return JSONTypeIdentifier
}

// GetTypeParams implements TypeInfo interface.
func (ti *jsonType) GetTypeParams() map[string]string {
	return nil
}

// IsValid implements TypeInfo interface.
func (ti *jsonType) IsValid(v types.Value) bool {
	switch v.(type) {
	case types.JSONDoc:
		return true
	case types.Null:
		return true
	default:
		return false
	}
}

// NomsKind implements TypeInfo interface.
func (ti *jsonType) NomsKind() types.NomsKind {
	return types.JSONDocKind
}

// ParseValue implements TypeInfo interface.
func (ti *jsonType) ParseValue(ctx context.Context, vrw types.ValueReadWriter, str *string) (types.Value, error) {
	if str == nil {
		return types.NullValue, nil
	}
	// TODO(andy) 2 step conversion here gonna be slow
	return ti.ConvertValueToNomsValue(context.Background(), nil, *str)
}

// Promote implements TypeInfo interface.
func (ti *jsonType) Promote() TypeInfo {
	return &jsonType{ti.jsonType.Promote().(sql.JsonType)}
}

// String implements TypeInfo interface.
func (ti *jsonType) String() string {
	return "JSON"
}

// ToSqlType implements TypeInfo interface.
func (ti *jsonType) ToSqlType() sql.Type {
	return ti.jsonType
}

// jsonTypeConverter is an internal function for GetTypeConverter that handles the specific type as the source TypeInfo.
func jsonTypeConverter(ctx context.Context, src *jsonType, destTi TypeInfo) (tc TypeConverter, needsConversion bool, err error) {
	switch dest := destTi.(type) {
	case *bitType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *boolType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *datetimeType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *decimalType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *enumType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *floatType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *inlineBlobType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *intType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *jsonType:
		return wrapIsValid(dest.IsValid, src, dest)
	case *setType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *timeType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *uintType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *uuidType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *varBinaryType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *varStringType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	case *yearType:
		return wrapConvertValueToNomsValue(dest.ConvertValueToNomsValue)
	default:
		return nil, false, UnhandledTypeConversion.New(src.String(), destTi.String())
	}
}
