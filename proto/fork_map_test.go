// Copyright 2026 The raftseg authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package proto_test

import (
	"testing"

	"github.com/raftseg/protobuf-go/proto"
	"github.com/raftseg/protobuf-go/reflect/protodesc"
	"github.com/raftseg/protobuf-go/reflect/protoreflect"
	"github.com/raftseg/protobuf-go/types/descriptorpb"
	"github.com/raftseg/protobuf-go/types/dynamicpb"
)

// A map entry holding a key and then a key of another wire type panicked
// in unmarshalMap ("type mismatch: cannot convert nil to map key"), on the
// reflection path that dynamicpb messages take. Google's library must now
// decode it as it decodes the same entry into generated code: the second
// key is an unknown field, and the first is kept. raftseg/protobuf-go-lite's
// differential test found it.
func TestMapEntryKeyOfAnotherWireType(t *testing.T) {
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:   proto.String("fork_map.proto"),
		Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("M"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("m"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
				TypeName: proto.String(".M.MEntry"),
				JsonName: proto.String("m"),
			}},
			NestedType: []*descriptorpb.DescriptorProto{{
				Name: proto.String("MEntry"),
				Field: []*descriptorpb.FieldDescriptorProto{{
					Name:     proto.String("key"),
					Number:   proto.Int32(1),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					JsonName: proto.String("key"),
				}, {
					Name:     proto.String("value"),
					Number:   proto.Int32(2),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
					JsonName: proto.String("value"),
				}},
				Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
			}},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	md := fd.Messages().ByName("M")
	// m: {key: "a", key: varint 0}: field 1, 5 bytes: 0a 01 61 (key "a"), 08 00.
	in := []byte{0x0a, 0x05, 0x0a, 0x01, 0x61, 0x08, 0x00}
	m := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(in, m); err != nil {
		t.Fatal(err)
	}
	mp := m.Get(md.Fields().ByName("m")).Map()
	if mp.Len() != 1 {
		t.Fatalf("map has %d entries, want 1", mp.Len())
	}
	mp.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		if k.String() != "a" || v.Int() != 0 {
			t.Errorf("entry %q: %v, want \"a\": 0", k.String(), v.Int())
		}
		return true
	})
}
