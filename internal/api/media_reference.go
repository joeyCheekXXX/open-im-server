package api

import (
	"fmt"
	"github.com/openimsdk/open-im-server/v3/pkg/apistruct"
	"strings"
)

// validateMediaReferences 保留原 URL 消息；空 URL 必须携带客户端内容 MD5。
func validateMediaReferences(data any) error {
	valid := func(url, id string) bool {
		if strings.TrimSpace(url) != "" {
			return true
		}
		if len(id) != 32 {
			return false
		}
		for _, c := range id {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	ok := true
	switch p := data.(type) {
	case apistruct.OANotificationElem:
		if p.PictureElem != nil {
			if err := validateMediaReferences(*p.PictureElem); err != nil {
				return err
			}
		}
		if p.SoundElem != nil {
			if err := validateMediaReferences(*p.SoundElem); err != nil {
				return err
			}
		}
		if p.VideoElem != nil {
			if err := validateMediaReferences(*p.VideoElem); err != nil {
				return err
			}
		}

	case apistruct.PictureElem:
		ok = valid(p.SourcePicture.Url, p.SourcePicture.UUID) && valid(p.BigPicture.Url, p.BigPicture.UUID) && valid(p.SnapshotPicture.Url, p.SnapshotPicture.UUID)
	case apistruct.SoundElem:
		ok = valid(p.SourceURL, p.UUID)
	case apistruct.VideoElem:
		ok = valid(p.VideoURL, p.VideoUUID) && valid(p.SnapshotURL, p.SnapshotUUID)
	}
	if !ok {
		return fmt.Errorf("media requires a URL or a lowercase MD5 reference")
	}
	return nil
}
