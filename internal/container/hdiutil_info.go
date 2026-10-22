package container

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
)

// HdiutilImage is one mounted volume reported by hdiutil info -plist.
// A system entity that has no mount point is a disk or partition
// header, not a volume, and is omitted.
type HdiutilImage struct {
	// ImagePath is the image-path value, the disk image that was
	// attached.
	ImagePath string
	// ShadowPath is the shadow-path value. A read-only attach omits
	// it, and ShadowPath is then empty.
	ShadowPath string
	// MountPoint is the system entity's mount-point value.
	MountPoint string
}

// ParseHdiutilInfo reads an hdiutil info -plist document. The parser
// accepts the XML plist subset hdiutil writes (dict, array, key,
// string) and ignores the other plist types. It has no build tag, so
// a Linux test can feed a fixture without executing hdiutil.
func ParseHdiutilInfo(data []byte) ([]HdiutilImage, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, errors.New("container: hdiutil info plist is empty")
	}
	var root plistNode
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("container: hdiutil info plist: %w", err)
	}
	return imagesFromPlist(root)
}

// plistNode is one XML element. Plist dicts store keys and values as
// siblings, so the decoder keeps the child list and the walker pairs
// them.
type plistNode struct {
	name string
	text string
	kids []plistNode
}

func (n *plistNode) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	n.name = start.Name.Local
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.CharData:
			n.text += string(t)
		case xml.StartElement:
			var child plistNode
			if err := child.UnmarshalXML(dec, t); err != nil {
				return err
			}
			n.kids = append(n.kids, child)
		case xml.EndElement:
			return nil
		case xml.Comment:
		}
	}
}

func imagesFromPlist(root plistNode) ([]HdiutilImage, error) {
	dict := root
	if root.name == "plist" {
		dict = plistChild(root, "dict")
	}
	if dict.name != "dict" {
		return nil, errors.New("container: hdiutil info plist has no dictionary")
	}
	images := dictValue(dict, "images")
	if images.name == "" {
		return nil, nil
	}
	if images.name != "array" {
		return nil, errors.New("container: hdiutil info images is not an array")
	}
	var out []HdiutilImage
	for _, image := range images.kids {
		if image.name != "dict" {
			continue
		}
		out = append(out, volumesOf(image)...)
	}
	return out, nil
}

// volumesOf lists the mount points of one attached image. The mount
// point lives on a system entity. An image-level mount-point is
// accepted for a short document that omits system entities.
func volumesOf(image plistNode) []HdiutilImage {
	path := stringValue(image, "image-path")
	shadow := stringValue(image, "shadow-path")
	var out []HdiutilImage
	entities := dictValue(image, "system-entities")
	if entities.name == "array" {
		for _, entity := range entities.kids {
			if entity.name != "dict" {
				continue
			}
			point := stringValue(entity, "mount-point")
			if point == "" {
				continue
			}
			out = append(out, HdiutilImage{
				ImagePath:  path,
				ShadowPath: shadow,
				MountPoint: point,
			})
		}
	}
	if len(out) == 0 {
		if point := stringValue(image, "mount-point"); point != "" {
			out = append(out, HdiutilImage{
				ImagePath:  path,
				ShadowPath: shadow,
				MountPoint: point,
			})
		}
	}
	return out
}

func dictValue(dict plistNode, key string) plistNode {
	want := false
	for _, child := range dict.kids {
		if child.name == "key" {
			want = strings.TrimSpace(child.text) == key
			continue
		}
		if want {
			return child
		}
	}
	return plistNode{}
}

func stringValue(dict plistNode, key string) string {
	node := dictValue(dict, key)
	if node.name != "string" {
		return ""
	}
	return node.text
}

func plistChild(parent plistNode, name string) plistNode {
	for _, child := range parent.kids {
		if child.name == name {
			return child
		}
	}
	return plistNode{}
}
