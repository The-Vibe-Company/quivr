package main

import (
	"encoding/json"
	"mime"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const (
	Kind              = "object_storage_prefix"
	checkpointKey     = "key"
	checkpointTime    = "timestamp"
	maxObjectBytesCap = int64(25 << 20)
	maxPageBytes      = int64(64 << 20)
	maxSessions       = 16
	maxKeyBytes       = 1024
)

type prefixConfig struct {
	Bucket                string            `json:"bucket"`
	Prefix                string            `json:"prefix"`
	Region                string            `json:"region"`
	Endpoint              string            `json:"endpoint"`
	ObjectPatterns        []string          `json:"object_patterns"`
	ExcludePatterns       []string          `json:"exclude_patterns"`
	CheckpointMode        string            `json:"checkpoint_mode"`
	MediaType             string            `json:"media_type"`
	MediaTypeByExtension  map[string]string `json:"media_type_by_extension"`
	AllowedMediaTypes     []string          `json:"allowed_media_types"`
	RecordKeyPattern      string            `json:"record_key_pattern"`
	SourcePositionPattern string            `json:"source_position_pattern"`
	BatchSize             int               `json:"batch_size"`
	Concurrency           int               `json:"concurrency"`
	MaxObjectBytes        int64             `json:"max_object_bytes"`
	SkipOversized         *bool             `json:"skip_oversized"`

	includes  []*regexp.Regexp
	excludes  []*regexp.Regexp
	allowedMT map[string]bool
	extMT     map[string]string
	keyRe     *regexp.Regexp
	posRe     *regexp.Regexp
}

var (
	decimal   = regexp.MustCompile(`^[0-9]+$`)
	mediaType = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)
)

var builtinMediaTypes = map[string]string{
	".txt":      "text/plain",
	".text":     "text/plain",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".html":     "text/html",
	".htm":      "text/html",
	".csv":      "text/csv",
	".tsv":      "text/tab-separated-values",
	".json":     "application/json",
	".xml":      "application/xml",
	".pdf":      "application/pdf",
	".png":      "image/png",
	".jpg":      "image/jpeg",
	".jpeg":     "image/jpeg",
	".webp":     "image/webp",
	".gif":      "image/gif",
	".tif":      "image/tiff",
	".tiff":     "image/tiff",
	".bmp":      "image/bmp",
	".svg":      "image/svg+xml",
}

// glob compiles a slash-aware glob where ** spans directories.
func glob(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?s)^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func decodeConfig(raw json.RawMessage) (prefixConfig, error) {
	skip := true
	c := prefixConfig{
		ObjectPatterns: []string{"**"},
		CheckpointMode: checkpointKey,
		BatchSize:      100,
		Concurrency:    1,
		MaxObjectBytes: maxObjectBytesCap,
		SkipOversized:  &skip,
	}
	if json.Unmarshal(raw, &c) != nil ||
		c.Bucket == "" || c.Region == "" ||
		c.BatchSize < 1 || c.BatchSize > 1000 ||
		c.Concurrency < 1 || c.Concurrency > 32 ||
		c.MaxObjectBytes < 1 || c.MaxObjectBytes > maxObjectBytesCap ||
		len(c.ObjectPatterns) == 0 {
		return c, quivrplugin.SourceError("invalid_config", "invalid prefix connector configuration")
	}
	if c.CheckpointMode != checkpointKey && c.CheckpointMode != checkpointTime {
		return c, quivrplugin.SourceError("invalid_config", "checkpoint_mode must be key or timestamp")
	}
	if c.MediaType != "" && !mediaType.MatchString(c.MediaType) {
		return c, quivrplugin.SourceError("invalid_config", "media_type is invalid")
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return c, quivrplugin.SourceError("invalid_config", "endpoint must be an HTTP or HTTPS origin")
		}
	}
	for _, p := range c.ObjectPatterns {
		r, err := glob(p)
		if err != nil {
			return c, quivrplugin.SourceError("invalid_config", "invalid object pattern")
		}
		c.includes = append(c.includes, r)
	}
	for _, p := range c.ExcludePatterns {
		r, err := glob(p)
		if err != nil {
			return c, quivrplugin.SourceError("invalid_config", "invalid exclude pattern")
		}
		c.excludes = append(c.excludes, r)
	}
	if len(c.MediaTypeByExtension) > 0 {
		c.extMT = make(map[string]string, len(c.MediaTypeByExtension))
		for ext, mt := range c.MediaTypeByExtension {
			if !strings.HasPrefix(ext, ".") || !mediaType.MatchString(mt) {
				return c, quivrplugin.SourceError("invalid_config", "invalid media_type_by_extension entry")
			}
			c.extMT[strings.ToLower(ext)] = mt
		}
	}
	if len(c.AllowedMediaTypes) > 0 {
		c.allowedMT = make(map[string]bool, len(c.AllowedMediaTypes))
		for _, mt := range c.AllowedMediaTypes {
			if !mediaType.MatchString(mt) {
				return c, quivrplugin.SourceError("invalid_config", "invalid allowed_media_types entry")
			}
			c.allowedMT[mt] = true
		}
	}
	for _, row := range []struct {
		raw  string
		dest **regexp.Regexp
	}{{c.RecordKeyPattern, &c.keyRe}, {c.SourcePositionPattern, &c.posRe}} {
		if row.raw == "" {
			continue
		}
		re, err := regexp.Compile(row.raw)
		if err != nil || re.NumSubexp() != 1 {
			return c, quivrplugin.SourceError("invalid_config", "identity patterns must contain exactly one capture group")
		}
		*row.dest = re
	}
	return c, nil
}

func (c prefixConfig) skipOversized() bool {
	return c.SkipOversized == nil || *c.SkipOversized
}

func (c prefixConfig) acceptsKey(key string) bool {
	if key == "" || strings.HasSuffix(key, "/") || !strings.HasPrefix(key, c.Prefix) {
		return false
	}
	matched := false
	for _, re := range c.includes {
		if re.MatchString(key) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, re := range c.excludes {
		if re.MatchString(key) {
			return false
		}
	}
	return true
}

func (c prefixConfig) resolveMediaType(key, contentType string) string {
	if c.MediaType != "" {
		return c.MediaType
	}
	ext := strings.ToLower(path.Ext(key))
	if mt, ok := c.extMT[ext]; ok {
		return mt
	}
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		parsed = strings.ToLower(parsed)
		if parsed != "" && parsed != "application/octet-stream" && parsed != "binary/octet-stream" && mediaType.MatchString(parsed) {
			return parsed
		}
	}
	if mt, ok := builtinMediaTypes[ext]; ok {
		return mt
	}
	return "application/octet-stream"
}

func (c prefixConfig) allowsMediaType(mt string) bool {
	if len(c.allowedMT) == 0 {
		return true
	}
	return c.allowedMT[mt]
}

func (c prefixConfig) identity(key string, ordinal int64) (string, string, error) {
	decoded, err := url.PathUnescape(key)
	if err != nil || !utf8.ValidString(decoded) || strings.ContainsRune(decoded, 0) {
		return "", "", quivrplugin.SourceError("invalid_object_key", "object key must decode to valid UTF-8 without NUL")
	}
	recordKey := strings.TrimPrefix(strings.TrimPrefix(decoded, c.Prefix), "/")
	if recordKey == "" {
		recordKey = decoded
	}
	pos := strconv.FormatInt(ordinal, 10)
	for _, row := range []struct {
		re  *regexp.Regexp
		out *string
	}{{c.keyRe, &recordKey}, {c.posRe, &pos}} {
		if row.re != nil {
			m := row.re.FindStringSubmatch(decoded)
			if len(m) != 2 || m[1] == "" {
				return "", "", quivrplugin.SourceError("object_identity_mismatch", "object key does not match an identity capture")
			}
			*row.out = m[1]
		}
	}
	if recordKey == "" || len(recordKey) > maxKeyBytes || len(pos) > 256 || !decimal.MatchString(pos) {
		return "", "", quivrplugin.SourceError("invalid_object_identity", "record key or numeric source position is invalid")
	}
	return recordKey, pos, nil
}
