package weaviate

import (
	"context"
	"errors"
	"sync"

	wv "github.com/weaviate/weaviate-go-client/v6"
	"github.com/weaviate/weaviate-go-client/v6/collections"
	"github.com/weaviate/weaviate-go-client/v6/collections/compression"
	"github.com/weaviate/weaviate-go-client/v6/collections/vectorindex"
	"github.com/weaviate/weaviate-go-client/v6/modules/selfprovided"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

func flag(v bool) *bool { return &v }

// searchable declares a text property keyword search scores (BM25).
func searchable(name string) collections.Property {
	return collections.Property{Name: name, DataType: collections.DataTypeText, Tokenization: collections.TokenizationWord, IndexSearchable: flag(true)}
}

// filterable declares an exact-match text property that search never scores.
func filterable(name string) collections.Property {
	return collections.Property{Name: name, DataType: collections.DataTypeText, Tokenization: collections.TokenizationField, IndexFilterable: flag(true), IndexSearchable: flag(false)}
}

// distance maps a registry metric to the Weaviate distance.
func distance(metric string) vectorindex.Distance {
	switch metric {
	case "dot":
		return vectorindex.DistanceDot
	case "l2":
		return vectorindex.DistanceL2Squared
	}
	return vectorindex.DistanceCosine
}

// rotational is RQ with a rescore limit that is always sent: the client
// library's own type omits a zero, and the index then applies its default.
type rotational struct {
	Bits         int  `json:"bits"`
	RescoreLimit int  `json:"rescoreLimit"`
	Cache        bool `json:"cache,omitempty"`
	Centering    bool `json:"centering,omitempty"`
}

func (rotational) Name() compression.Type { return "rq" }

// vectorConfig declares an HNSW named vector stored as index says; a nil
// index (a space recorded before index settings) gets the default. "none"
// also opts out of a quantization the database applies by default.
func vectorConfig(metric string, index *content.VectorIndex) collections.VectorConfig {
	if index == nil {
		index = &content.DefaultVectorIndex
	}
	config := collections.VectorConfig{Vectorizer: selfprovided.Vectorizer, Index: vectorindex.HNSW{Distance: distance(metric)}}
	switch index.Quantization {
	case content.QuantizationRQ8:
		config.Compression = rotational{Bits: 8, RescoreLimit: index.RescoreLimit}
	case content.QuantizationRQ1:
		// Without a limit the index keeps its own default for 1-bit codes.
		config.Compression = compression.RQ{Bits: 1, RescoreLimit: index.RescoreLimit}
	case content.QuantizationNone:
		config.SkipDefaultCompression = true
	}
	return config
}

// rq8Setting identifies a mutable RQ-8 limit using the generation's recorded
// setting. Other compression settings and unrelated vector options stay intact.
func rq8Setting(vector collections.VectorConfig, index *content.VectorIndex) (compression.RQ, int, bool) {
	if index != nil && index.Quantization != content.QuantizationRQ8 {
		return compression.RQ{}, 0, false
	}
	rq, ok := vector.Compression.(compression.RQ)
	if _, hnsw := vector.Index.(vectorindex.HNSW); !hnsw || !ok || rq.Bits != 8 {
		return compression.RQ{}, 0, false
	}
	limit := 0
	if index != nil {
		limit = index.RescoreLimit
	}
	return rq, limit, true
}

// collection reads a collection's schema; found is false only on a definite 404.
func (s *Store) collection(ctx context.Context, name string) (schema *collections.Collection, found bool, err error) {
	err = s.call(ctx, func(ctx context.Context, c *wv.Client) error {
		schema, err = c.Collections.Use(name).Config.Get(ctx)
		return err
	})
	if notFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, failed(err)
	}
	return schema, schema != nil, nil
}

// setRescoreLimit changes only the RQ limit of one named vector.
func (s *Store) setRescoreLimit(ctx context.Context, collection, vector string, limit int) error {
	return failed(s.call(ctx, func(ctx context.Context, c *wv.Client) error {
		return c.Collections.Use(collection).Config.UpdateVectorConfig(ctx, vector, func(config *collections.VectorConfig) {
			if rq, ok := config.Compression.(compression.RQ); ok {
				config.Compression = rotational{Bits: rq.Bits, RescoreLimit: limit, Cache: rq.Cache, Centering: rq.Centering}
			}
		})
	}))
}

// ensureVectors adds missing spaces and reconciles mutable RQ-8 limits before
// caching a generation's named vectors. Vector names and graph contents stay
// unchanged, including for generations recorded before index settings.
func (s *Store) ensureVectors(ctx context.Context, g content.Generation) error {
	if !g.SpacesProjected {
		return nil
	}
	known := s.vectors
	if known == nil {
		known = &sync.Map{}
	}
	missing := false
	for _, sp := range g.Spaces {
		if _, ok := known.Load(g.Collection + "/" + s.VectorName(g, sp.ID)); !ok {
			missing = true
		}
	}
	if !missing {
		return nil
	}
	schema, found, err := s.collection(ctx, g.Collection)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("projection request failed")
	}
	changed := false
	limits := map[string]int{}
	for _, sp := range g.Spaces {
		name := s.VectorName(g, sp.ID)
		current, ok := schema.Vectors[name]
		if !ok {
			config := vectorConfig(sp.Metric, sp.Index)
			err = s.call(ctx, func(ctx context.Context, c *wv.Client) error {
				return c.Collections.Use(g.Collection).Config.AddVector(ctx, name, config)
			})
			// Another process may have added the same vector first; the
			// read below decides.
			changed = true
			if sp.Index == nil || sp.Index.Quantization == content.QuantizationRQ8 {
				limit := 0
				if sp.Index != nil {
					limit = sp.Index.RescoreLimit
				}
				limits[name] = limit
			}
		} else if rq, limit, ok := rq8Setting(current, sp.Index); ok {
			limits[name] = limit
			if rq.RescoreLimit != limit {
				if err = s.setRescoreLimit(ctx, g.Collection, name, limit); err != nil {
					return err
				}
				changed = true
			}
		}
	}
	if changed {
		added := err
		if schema, found, err = s.collection(ctx, g.Collection); err != nil {
			return err
		}
		if !found {
			return errors.New("projection request failed")
		}
		for _, sp := range g.Spaces {
			if _, ok := schema.Vectors[s.VectorName(g, sp.ID)]; !ok && added != nil {
				return failed(added)
			}
		}
	}
	for _, sp := range g.Spaces {
		name := s.VectorName(g, sp.ID)
		current, ok := schema.Vectors[name]
		if !ok {
			return errors.New("projection named vector missing")
		}
		if limit, check := limits[name]; check {
			rq, _, ok := rq8Setting(current, sp.Index)
			if !ok || rq.RescoreLimit != limit {
				return errors.New("projection named vector rescore setting differs")
			}
		}
	}
	for _, sp := range g.Spaces {
		known.Store(g.Collection+"/"+s.VectorName(g, sp.ID), true)
	}
	return nil
}

// Reconcile additive schemas once per process, including concurrent creation.
func (s *Store) ensureProperties(ctx context.Context, collection string, props []collections.Property) error {
	s.metadataMu.Lock()
	if s.metadataProperties == nil {
		s.metadataProperties = map[string]bool{}
	}
	var missing []collections.Property
	for _, p := range props {
		if !s.metadataProperties[collection+"/"+p.Name] {
			missing = append(missing, p)
		}
	}
	s.metadataMu.Unlock()
	if len(missing) == 0 {
		return nil
	}
	present, err := s.propertyNames(ctx, collection)
	if err != nil {
		return err
	}
	for _, p := range missing {
		if !present[p.Name] {
			if err := s.addProperty(ctx, collection, p); err != nil {
				present, readErr := s.propertyNames(ctx, collection)
				if readErr != nil {
					return readErr
				}
				if !present[p.Name] {
					return err
				}
			}
		}
		s.metadataMu.Lock()
		s.metadataProperties[collection+"/"+p.Name] = true
		s.metadataMu.Unlock()
	}
	return nil
}

func (s *Store) propertyNames(ctx context.Context, collection string) (map[string]bool, error) {
	schema, found, err := s.collection(ctx, collection)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("projection request failed")
	}
	present := map[string]bool{}
	for _, p := range schema.Properties {
		present[p.Name] = true
	}
	return present, nil
}

func (s *Store) addProperty(ctx context.Context, collection string, property collections.Property) error {
	return failed(s.call(ctx, func(ctx context.Context, c *wv.Client) error {
		return c.Collections.Use(collection).Config.AddProperty(ctx, property)
	}))
}

func (s *Store) Bootstrap(ctx context.Context, collection string) error {
	if !className.MatchString(collection) {
		return errors.New("invalid projection route")
	}
	existing, found, err := s.collection(ctx, collection)
	if err != nil {
		return err
	}
	if found {
		present := map[string]bool{}
		for _, p := range existing.Properties {
			present[p.Name] = true
		}
		// A collection created before source routing, named spaces, or lexical
		// plugin metadata gains the missing properties. Older objects lack a
		// value, so their immutable anchors keep their original bytes.
		var missing []collections.Property
		for _, property := range []collections.Property{
			filterable(projectionPluginProperty),
			filterable(sourceMediaTypeProperty),
			filterable(sourceNamespaceProperty),
			searchable(lexicalProperty),
		} {
			if !present[property.Name] {
				missing = append(missing, property)
			}
		}
		// Another process may be adding the same properties while it starts.
		if err = s.ensureProperties(ctx, collection, missing); err != nil {
			return err
		}
		// Bootstrap owns only the legacy default. Other named vectors require
		// their generation's recorded settings, reconciled by ensureVectors.
		if rq, _, ok := rq8Setting(existing.Vectors[legacyVector], nil); ok && rq.RescoreLimit != 0 {
			if err = s.setRescoreLimit(ctx, collection, legacyVector, 0); err != nil {
				return err
			}
			if existing, found, err = s.collection(ctx, collection); err != nil {
				return err
			}
			if !found {
				return errors.New("projection request failed")
			}
			if rq, _, ok = rq8Setting(existing.Vectors[legacyVector], nil); !ok || rq.RescoreLimit != 0 {
				return errors.New("projection legacy vector rescore setting differs")
			}
		}
		return nil
	}
	var properties []collections.Property
	for _, name := range []string{"organization", "corpusId", "generationId", "segmentId", "versionId", "segmentationId", projectionPluginProperty, sourceMediaTypeProperty, sourceNamespaceProperty} {
		properties = append(properties, filterable(name))
	}
	for _, name := range []string{"title", "body", lexicalProperty} {
		properties = append(properties, searchable(name))
	}
	return failed(s.call(ctx, func(ctx context.Context, c *wv.Client) error {
		_, err := c.Collections.Create(ctx, collections.Collection{
			Name:          collection,
			Properties:    properties,
			Vectors:       map[string]collections.VectorConfig{legacyVector: vectorConfig("cosine", nil)},
			InvertedIndex: &collections.InvertedIndexConfig{Stopwords: &collections.StopwordConfig{Preset: "none"}},
			Replication:   &collections.ReplicationConfig{Factor: 1},
		})
		return err
	}))
}
