package main

import (
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const Kind = "object_storage_archive"
const maxMemberBytes int64 = 25 << 20
const maxPageBytes int64 = 64 << 20
const maxStreams = 8

type archiveConfig struct {
	Bucket                string   `json:"bucket"`
	Prefix                string   `json:"prefix"`
	Region                string   `json:"region"`
	Endpoint              string   `json:"endpoint"`
	ArchivePatterns       []string `json:"archive_patterns"`
	MemberPattern         string   `json:"member_pattern"`
	MediaType             string   `json:"media_type"`
	RecordKeyPattern      string   `json:"record_key_pattern"`
	SourcePositionPattern string   `json:"source_position_pattern"`
	BatchSize             int      `json:"batch_size"`
	Concurrency           int      `json:"concurrency"`
	MembersTotal          *int64   `json:"members_total"`
	archives              []*regexp.Regexp
	members               *regexp.Regexp
	key, position         *regexp.Regexp
}

// Globs are slash-aware; ** matches across directory boundaries.
func glob(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
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
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
func decodeConfig(raw json.RawMessage) (archiveConfig, error) {
	c := archiveConfig{ArchivePatterns: []string{"**/*.tar.gz", "**/*.zip"}, MemberPattern: "**", BatchSize: 100, Concurrency: 1}
	if json.Unmarshal(raw, &c) != nil || c.Bucket == "" || c.Region == "" || c.MediaType == "" || c.BatchSize < 1 || c.BatchSize > 1000 || c.Concurrency < 1 || c.Concurrency > 32 || len(c.ArchivePatterns) == 0 || c.MemberPattern == "" || (c.MembersTotal != nil && *c.MembersTotal < 0) {
		return c, quivrplugin.SourceError("invalid_config", "invalid archive configuration")
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, quivrplugin.SourceError("invalid_config", "endpoint must be an HTTP or HTTPS origin")
		}
	}
	for _, p := range c.ArchivePatterns {
		r, err := glob(p)
		if err != nil {
			return c, quivrplugin.SourceError("invalid_config", "invalid archive pattern")
		}
		c.archives = append(c.archives, r)
	}
	c.members, _ = glob(c.MemberPattern)
	for _, row := range []struct {
		raw  string
		dest **regexp.Regexp
	}{{c.RecordKeyPattern, &c.key}, {c.SourcePositionPattern, &c.position}} {
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
func (c archiveConfig) acceptsArchive(key string) bool {
	if !strings.HasSuffix(key, ".tar.gz") && !strings.HasSuffix(key, ".zip") {
		return false
	}
	for _, re := range c.archives {
		if re.MatchString(key) {
			return true
		}
	}
	return false
}
func (c archiveConfig) identity(name string, ordinal int64) (string, string, error) {
	decoded, err := url.PathUnescape(name)
	if err != nil || !utf8.ValidString(decoded) || strings.ContainsRune(decoded, 0) {
		return "", "", quivrplugin.SourceError("invalid_member_path", "member path must decode to valid UTF-8 without NUL")
	}
	key, pos := path.Base(decoded), strconv.FormatInt(ordinal, 10)
	for _, row := range []struct {
		re  *regexp.Regexp
		out *string
	}{{c.key, &key}, {c.position, &pos}} {
		if row.re != nil {
			m := row.re.FindStringSubmatch(decoded)
			if len(m) != 2 || m[1] == "" {
				return "", "", quivrplugin.SourceError("member_identity_mismatch", "member path does not match an identity capture")
			}
			*row.out = m[1]
		}
	}
	if key == "" || len(key) > 1024 || len(pos) > 256 || !decimal.MatchString(pos) {
		return "", "", quivrplugin.SourceError("invalid_member_identity", "record key or numeric source position is invalid")
	}
	return key, pos, nil
}

var decimal = regexp.MustCompile(`^[0-9]+$`)
