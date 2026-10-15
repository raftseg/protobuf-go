// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Modified by the raftseg authors: import paths rewritten for this
// fork's module.

package tag_test

import (
	"reflect"
	"testing"

	"github.com/raftseg/protobuf-go/internal/encoding/tag"
	"github.com/raftseg/protobuf-go/internal/filedesc"
	"github.com/raftseg/protobuf-go/proto"
	"github.com/raftseg/protobuf-go/reflect/protodesc"
	"github.com/raftseg/protobuf-go/reflect/protoreflect"
)

func Test(t *testing.T) {
	fd := new(filedesc.Field)
	fd.L0.ParentFile = filedesc.SurrogateProto3
	fd.L0.FullName = "foo_field"
	fd.L1.Number = 1337
	fd.L1.Cardinality = protoreflect.Repeated
	fd.L1.Kind = protoreflect.BytesKind
	fd.L1.Default = filedesc.DefaultValue(protoreflect.ValueOf([]byte("hello, \xde\xad\xbe\xef\n")), nil)
	fd.L1.EditionFeatures = fd.L0.ParentFile.L1.EditionFeatures

	// Marshal test.
	gotTag := tag.Marshal(fd, "")
	wantTag := `bytes,1337,rep,name=foo_field,json=fooField,proto3,def=hello, \336\255\276\357\n`
	if gotTag != wantTag {
		t.Errorf("Marshal() = `%v`, want `%v`", gotTag, wantTag)
	}

	// Unmarshal test.
	gotFD := tag.Unmarshal(wantTag, reflect.TypeOf([]byte{}), nil)
	wantFD := fd
	if !proto.Equal(protodesc.ToFieldDescriptorProto(gotFD), protodesc.ToFieldDescriptorProto(wantFD)) {
		t.Errorf("Umarshal() mismatch:\ngot  %v\nwant %v", gotFD, wantFD)
	}
	if gotFD.(*filedesc.Field).L1.EditionFeatures != wantFD.L1.EditionFeatures {
		t.Errorf("Unmarshal() did not set proto3 edition features")
	}
}
