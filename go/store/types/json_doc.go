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
//
// This file incorporates work covered by the following copyright and
// permission notice:
//
// Copyright 2016 Attic Labs, Inc. All rights reserved.
// Licensed under the Apache License, version 2.0:
// http://www.apache.org/licenses/LICENSE-2.0

package types

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dolthub/dolt/go/store/d"
)

type JSONDoc struct {
	valueImpl
}

func NewJSONDoc(nbf *NomsBinFormat, value Value) (JSONDoc, error) {
	w := newBinaryNomsWriter()
	if err := JSONDocKind.writeTo(&w, nbf); err != nil {
		return EmptyJSONDoc(nbf), err
	}

	w.writeCount(uint64(1))

	if err := value.writeTo(&w, nbf); err != nil {
		return EmptyJSONDoc(nbf), err
	}

	vrw := value.(valueReadWriter).valueReadWriter()
	return JSONDoc{valueImpl{vrw, nbf, w.data(), nil}}, nil
}

func EmptyJSONDoc(nbf *NomsBinFormat) JSONDoc {
	w := newBinaryNomsWriter()
	if err := JSONDocKind.writeTo(&w, nbf); err != nil {
		d.PanicIfError(err)
	}
	w.writeCount(uint64(0))

	return JSONDoc{valueImpl{nil, nbf, w.data(), nil}}
}

// readJSON reads the data provided by a decoder and moves the decoder forward.
func readJSON(nbf *NomsBinFormat, dec *valueDecoder) (JSONDoc, error) {
	start := dec.pos()

	k := dec.PeekKind()
	if k == NullKind {
		dec.skipKind()
		return EmptyJSONDoc(nbf), nil
	}
	if k != JSONDocKind {
		return JSONDoc{}, errors.New("current value is not a JSONDoc")
	}

	if err := skipJSON(nbf, dec); err != nil {
		return JSONDoc{}, err
	}

	end := dec.pos()
	return JSONDoc{valueImpl{dec.vrw, nbf, dec.byteSlice(start, end), nil}}, nil
}

func skipJSON(nbf *NomsBinFormat, dec *valueDecoder) error {
	dec.skipKind()
	count := dec.readCount()
	for i := uint64(0); i < count; i++ {
		err := dec.SkipValue(nbf)

		if err != nil {
			return err
		}
	}
	return nil
}


func walkJSON(nbf *NomsBinFormat, r *refWalker, cb RefCallback) error {
	r.skipKind()
	count := r.readCount()
	for i := uint64(0); i < count; i++ {
		err := r.walkValue(nbf, cb)

		if err != nil {
			return err
		}
	}
	return nil
}


// CopyOf creates a copy of a JSONDoc.  This is necessary in cases where keeping a reference to the original JSONDoc is
// preventing larger objects from being collected.
func (t JSONDoc) CopyOf(vrw ValueReadWriter) JSONDoc {
	buff := make([]byte, len(t.buff))
	offsets := make([]uint32, len(t.offsets))

	copy(buff, t.buff)
	copy(offsets, t.offsets)

	return JSONDoc{
		valueImpl{
			buff:    buff,
			offsets: offsets,
			vrw:     vrw,
			nbf:     t.nbf,
		},
	}
}

func (t JSONDoc) Empty() bool {
	return t.Len() == 0
}

func (t JSONDoc) Format() *NomsBinFormat {
	return t.format()
}

// Value interface
func (t JSONDoc) Value(ctx context.Context) (Value, error) {
	return t, nil
}

func (t JSONDoc) Inner() (Value, error) {
	dec := newValueDecoder(t.buff, t.vrw)
	dec.skipKind()
	dec.skipCount()
	return dec.readValue(t.nbf)
}

func (t JSONDoc) WalkValues(ctx context.Context, cb ValueCallback) error {
	val, err := t.Inner()
	if err != nil {
		return err
	}
	return val.WalkValues(ctx, cb)
}

func (t JSONDoc) typeOf() (*Type, error) {
	val, err := t.Inner()
	if err != nil {
		return nil, err
	}
	return val.typeOf()
}

func (t JSONDoc) Kind() NomsKind {
	return JSONDocKind
}

func (t JSONDoc) decoderSkipToFields() (valueDecoder, uint64) {
	dec := t.decoder()
	dec.skipKind()
	count := dec.readCount()
	return dec, count
}

func (t JSONDoc) Len() uint64 {
	return 1
}

func (t JSONDoc) isPrimitive() bool {
	return false
}

func (t JSONDoc) Less(nbf *NomsBinFormat, other LesserValuable) (bool, error) {
	otherJSONDoc, ok := other.(JSONDoc)
	if !ok {
		return JSONDocKind < other.Kind(), nil
	}

	cmp, err := t.Compare(otherJSONDoc)
	if err != nil {
		return false, err
	}

	return cmp == -1, nil
}

func (t JSONDoc) Compare(other JSONDoc) (int, error) {
	left, err := t.Inner()
	if err != nil {
		return 0, err
	}

	right, err := other.Inner()
	if err != nil {
		return 0, err
	}

	return compareJSON(left, right)
}

func (t JSONDoc) readFrom(nbf *NomsBinFormat, b *binaryNomsReader) (Value, error) {
	panic("unreachable")
}

func (t JSONDoc) skip(nbf *NomsBinFormat, b *binaryNomsReader) {
	panic("unreachable")
}

func (t JSONDoc) HumanReadableString() string {
	val, err := t.Inner()
	if err != nil {
		d.PanicIfError(err)
	}
	return fmt.Sprintf("JSON(%s)", val.HumanReadableString())
}

func compareJSON(a, b Value) (int, error) {
	aNull := a.Kind() == NullKind
	bNull := b.Kind() == NullKind
	if aNull && bNull {
		return 0, nil
	} else if aNull && !bNull {
		return -1, nil
	} else if !aNull && bNull {
		return 1, nil
	}

	switch a := a.(type) {
	case Bool:
		return compareJSONBool(a, b)
	case List:
		return compareJSONArray(a, b)
	case Map:
		return compareJSONObject(a, b)
	case String:
		return compareJSONString(a, b)
	case Float:
		return compareJSONNumber(a, b)
	default:
		return 0, fmt.Errorf("unexpected type: %v", a)
	}
}

func compareJSONBool(a Bool, b Value) (int, error) {
	switch b := b.(type) {
	case Bool:
		// The JSON false literal is less than the JSON true literal.
		if a == b {
			return 0, nil
		}
		if a {
			// a > b
			return 1, nil
		} else {
			// a < b
			return -1, nil
		}

	default:
		// a is higher precedence
		return 1, nil
	}
}

func compareJSONArray(a List, b Value) (int, error) {
	switch b := b.(type) {
	case Bool:
		// a is lower precedence
		return -1, nil

	case List:
		// Two JSON arrays are equal if they have the same length and values in corresponding positions in the arrays
		// are equal. If the arrays are not equal, their order is determined by the elements in the first position
		// where there is a difference. The array with the smaller value in that position is ordered first.

		// TODO(andy): this diverges from GMS
		aLess, err := a.Less(a.format(), b)
		if err != nil {
			return 0, err
		}
		if aLess {
			return -1, nil
		}

		bLess, err := b.Less(b.format(), a)
		if err != nil {
			return 0, err
		}
		if bLess {
			return 1, nil
		}

		return 0, nil

	default:
		// a is higher precedence
		return 1, nil
	}
}

func compareJSONObject(a Map, b Value) (int, error) {
	switch b := b.(type) {
	case
		Bool,
		List:
		// a is lower precedence
		return -1, nil

	case Map:
		// Two JSON objects are equal if they have the same set of keys, and each key has the same value in both
		// objects. The order of two objects that are not equal is unspecified but deterministic.

		// TODO(andy): this diverges from GMS
		aLess, err := a.Less(a.format(), b)
		if err != nil {
			return 0, err
		}
		if aLess {
			return -1, nil
		}

		bLess, err := b.Less(b.format(), a)
		if err != nil {
			return 0, err
		}
		if bLess {
			return 1, nil
		}

		return 0, nil

	default:
		// a is higher precedence
		return 1, nil
	}
}

func compareJSONString(a String, b Value) (int, error) {
	switch b := b.(type) {
	case
		Bool,
		List,
		Map:
		// a is lower precedence
		return -1, nil

	case String:
		return strings.Compare(string(a), string(b)), nil

	default:
		// a is higher precedence
		return 1, nil
	}
}

func compareJSONNumber(a Float, b Value) (int, error) {
	switch b := b.(type) {
	case
		Bool,
		List,
		Map,
		String:
		// a is lower precedence
		return -1, nil

	case Float:
		if a > b {
			return 1, nil
		} else if a < b {
			return -1, nil
		}
		return 0, nil

	default:
		// a is higher precedence
		return 1, nil
	}
}
