package api

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/openimsdk/open-im-server/v3/pkg/apistruct"
	"reflect"
	"testing"
)

func TestNativeMediaReferenceValidation(t *testing.T) {
	id := "d41d8cd98f00b204e9800998ecf8427e"
	pic := apistruct.PictureBaseInfo{UUID: id, Type: "image/jpeg", Size: 100, Width: 10, Height: 10}
	for _, data := range []any{apistruct.PictureElem{SourcePicture: pic, BigPicture: pic, SnapshotPicture: pic}, apistruct.SoundElem{UUID: id, Duration: 3, DataSize: 100}, apistruct.VideoElem{VideoUUID: id, SnapshotUUID: id, VideoSize: 100, VideoType: "video/mp4", Duration: 3, SnapshotWidth: 10, SnapshotHeight: 10}, apistruct.SoundElem{SourceURL: "https://legacy/voice", UUID: "legacy-id", Duration: 3}} {
		if err := validateMediaReferences(data); err != nil {
			t.Fatal(err)
		}
		if err := validator.New().Struct(data); err != nil {
			t.Fatal("remaining native validation rejected", err)
		}
	}
	for _, data := range []any{apistruct.SoundElem{}, apistruct.SoundElem{UUID: "bad"}, apistruct.SoundElem{UUID: "D41D8CD98F00B204E9800998ECF8427E"}, apistruct.SoundElem{SourceURL: "  "}, apistruct.VideoElem{VideoUUID: id}, apistruct.PictureElem{SourcePicture: pic, BigPicture: pic}} {
		if validateMediaReferences(data) == nil {
			t.Fatalf("accepted missing/invalid reference: %#v", data)
		}
	}
}

func TestNativeMediaRequestDecodingPreservesUUIDAndEmptyURL(t *testing.T) {
	m := NewMessageApi(nil, nil, nil)
	c := &gin.Context{}
	id := "08d6c05a21512a79a1dfeb9d2a8f262f"
	p := map[string]any{"uuid": id, "url": "", "type": "image/png", "size": 4, "width": 2, "height": 3}
	for _, tc := range []struct {
		kind    int32
		content map[string]any
	}{
		{102, map[string]any{"sourcePicture": p, "bigPicture": p, "snapshotPicture": p}},
		{103, map[string]any{"uuid": id, "sourceUrl": "", "dataSize": 4, "duration": 3}},
		{104, map[string]any{"videoUUID": id, "snapshotUUID": id, "videoUrl": "", "snapshotUrl": "", "videoType": "video/mp4", "videoSize": 4, "snapshotSize": 2, "duration": 3, "snapshotWidth": 2, "snapshotHeight": 3}},
	} {
		got, err := m.getSendMsgReq(c, apistruct.SendMsg{SendID: "sender", ContentType: tc.kind, Content: tc.content})
		if err != nil {
			t.Fatal(tc.kind, err)
		}
		var content map[string]any
		if err = json.Unmarshal(got.MsgData.Content, &content); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(content, toJSONMap(t, tc.content)) {
			t.Fatal("native fields changed", content)
		}
	}
	if _, err := m.getSendMsgReq(c, apistruct.SendMsg{ContentType: 103, Content: map[string]any{"sourceUrl": "", "duration": 3}}); err == nil {
		t.Fatal("missing UUID accepted")
	}
	if validateMediaReferences(apistruct.OANotificationElem{SoundElem: &apistruct.SoundElem{Duration: 3}}) == nil {
		t.Fatal("nested empty media bypassed validation")
	}
	if validator.New().Struct(apistruct.SoundElem{UUID: id, Duration: 0}) == nil {
		t.Fatal("duration validation relaxed")
	}
}
func toJSONMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	var m map[string]any
	if e = json.Unmarshal(b, &m); e != nil {
		t.Fatal(e)
	}
	return m
}
