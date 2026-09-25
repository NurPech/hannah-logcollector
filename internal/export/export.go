// Package export builds the downloadable log archive: a tar.gz with one plain-text
// file per source plus manifest.json.
package export

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	pb "github.com/NurPech/hannah-proto-go/v4"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
)

const manifestName = "manifest.json"

// Manifest describes what an archive contains — and what it deliberately leaves out.
type Manifest struct {
	GeneratedAt        string           `json:"generated_at"`
	Since              string           `json:"since,omitempty"`
	Until              string           `json:"until"`
	ExcludedCategories []string         `json:"excluded_categories"`
	Sources            []ManifestSource `json:"sources"`
}

type ManifestSource struct {
	Component string        `json:"component"`
	Instance  string        `json:"instance"`
	Version   string        `json:"version"`
	File      string        `json:"file,omitempty"` // empty when only gaps fall into the range
	Entries   int64         `json:"entries"`
	Oldest    string        `json:"oldest,omitempty"`
	Newest    string        `json:"newest,omitempty"`
	Gaps      []ManifestGap `json:"gaps,omitempty"`
}

// ManifestGap is a stretch of entries the component had to drop before shipping them.
type ManifestGap struct {
	Dropped int64  `json:"dropped"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// Build writes the archive for f to w. Per-source files are staged in tempDir,
// since tar needs each file's size before its content.
func Build(ctx context.Context, st *store.Store, f store.Filter, now time.Time, tempDir string, w io.Writer) error {
	if f.UntilMs == 0 {
		f.UntilMs = now.UnixMilli()
	}

	sources, err := st.AllSources(ctx)
	if err != nil {
		return fmt.Errorf("loading sources: %w", err)
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	manifest := map[int64]*ManifestSource{}
	source := func(id int64) *ManifestSource {
		if m, ok := manifest[id]; ok {
			return m
		}
		src := sources[id]
		m := &ManifestSource{Component: src.Component, Instance: src.Instance, Version: src.Version}
		manifest[id] = m
		return m
	}

	// Entries arrive ordered by source: stage each source's lines, add them to the
	// archive once the next source starts.
	var current *stagedFile
	finish := func() error {
		if current == nil {
			return nil
		}
		err := current.addTo(tw, now)
		current = nil
		return err
	}
	defer func() {
		if current != nil {
			current.discard()
		}
	}()

	err = st.EachEntry(ctx, f, func(e store.Entry) error {
		if current == nil || current.sourceID != e.SourceID {
			if err := finish(); err != nil {
				return err
			}
			m := source(e.SourceID)
			m.File = fileName(m.Component, m.Instance)
			staged, err := newStagedFile(tempDir, e.SourceID, m.File)
			if err != nil {
				return err
			}
			current = staged
			m.Oldest = formatMs(e.TimestampMs)
		}
		m := manifest[e.SourceID]
		m.Entries++
		m.Newest = formatMs(e.TimestampMs)
		return current.writeLine(e)
	})
	if err != nil {
		return fmt.Errorf("reading entries: %w", err)
	}
	if err := finish(); err != nil {
		return err
	}

	gaps, err := st.Gaps(ctx, f)
	if err != nil {
		return fmt.Errorf("reading gaps: %w", err)
	}
	for _, g := range gaps {
		m := source(g.SourceID)
		m.Gaps = append(m.Gaps, ManifestGap{Dropped: g.Dropped, From: formatMs(g.FromMs), To: formatMs(g.ToMs)})
	}

	if err := writeManifest(tw, buildManifest(manifest, f, now), now); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func buildManifest(sources map[int64]*ManifestSource, f store.Filter, now time.Time) Manifest {
	m := Manifest{
		GeneratedAt:        now.UTC().Format(time.RFC3339),
		Until:              formatMs(f.UntilMs),
		ExcludedCategories: []string{},
		Sources:            []ManifestSource{},
	}
	if f.SinceMs > 0 {
		m.Since = formatMs(f.SinceMs)
	}
	for _, c := range f.ExcludeCategories {
		m.ExcludedCategories = append(m.ExcludedCategories, CategoryName(c))
	}
	for _, s := range sources {
		m.Sources = append(m.Sources, *s)
	}
	sort.Slice(m.Sources, func(i, j int) bool {
		a, b := m.Sources[i], m.Sources[j]
		if a.Component != b.Component {
			return a.Component < b.Component
		}
		return a.Instance < b.Instance
	})
	return m
}

func writeManifest(tw *tar.Writer, m Manifest, now time.Time) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o644, Size: int64(len(data)), ModTime: now}); err != nil {
		return err
	}
	_, err = tw.Write(data)
	return err
}

// stagedFile collects one source's lines in a temp file until its size is known.
type stagedFile struct {
	sourceID int64
	name     string
	file     *os.File
}

func newStagedFile(dir string, sourceID int64, name string) (*stagedFile, error) {
	f, err := os.CreateTemp(dir, "export-*.log")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}
	return &stagedFile{sourceID: sourceID, name: name, file: f}, nil
}

func (s *stagedFile) writeLine(e store.Entry) error {
	_, err := io.WriteString(s.file, FormatLine(e))
	return err
}

func (s *stagedFile) addTo(tw *tar.Writer, now time.Time) error {
	defer s.discard()
	info, err := s.file.Stat()
	if err != nil {
		return err
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: s.name, Mode: 0o644, Size: info.Size(), ModTime: now}); err != nil {
		return err
	}
	_, err = io.Copy(tw, s.file)
	return err
}

func (s *stagedFile) discard() {
	s.file.Close()
	os.Remove(s.file.Name())
}

// FormatLine renders an entry as one line of the plain-text log file.
func FormatLine(e store.Entry) string {
	var b strings.Builder
	b.WriteString(formatMs(e.TimestampMs))
	b.WriteString(" ")
	fmt.Fprintf(&b, "%-8s ", LevelName(e.Level))
	if e.Logger != "" {
		b.WriteString(e.Logger)
		b.WriteString(": ")
	}
	b.WriteString(e.Message)
	if !strings.HasSuffix(e.Message, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func LevelName(level int32) string {
	return strings.TrimPrefix(pb.LogLevel(level).String(), "LOG_LEVEL_")
}

func CategoryName(category int32) string {
	return strings.TrimPrefix(pb.LogCategory(category).String(), "LOG_CATEGORY_")
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func fileName(component, instance string) string {
	name := unsafeChars.ReplaceAllString(component, "_")
	if instance != "" {
		name += "-" + unsafeChars.ReplaceAllString(instance, "_")
	}
	return name + ".log"
}

func formatMs(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}
