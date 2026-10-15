// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Modified by the raftseg authors: import paths rewritten for this
// fork's module.

package impl

import (
	"reflect"

	"github.com/raftseg/protobuf-go/reflect/protoreflect"
)

type EnumInfo struct {
	GoReflectType reflect.Type // int32 kind
	Desc          protoreflect.EnumDescriptor
}

func (t *EnumInfo) New(n protoreflect.EnumNumber) protoreflect.Enum {
	return reflect.ValueOf(n).Convert(t.GoReflectType).Interface().(protoreflect.Enum)
}
func (t *EnumInfo) Descriptor() protoreflect.EnumDescriptor { return t.Desc }
