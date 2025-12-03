// Package locations provides storage and retrieval of test location data.
package locations

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Location represents a test server location / data center.
type Location struct {
	IATA   string  `json:"iata"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	CCA2   string  `json:"cca2"`
	Region string  `json:"region"`
	City   string  `json:"city"`
}

// Store is the interface for accessing location data.
type Store interface {
	All() []Location
}

// FileStore loads locations from a JSON file at startup.
type FileStore struct {
	mu        sync.RWMutex
	locations []Location
}

// NewFileStore creates a new FileStore by loading locations from the given file path.
// If the file cannot be read or parsed, an error is returned.
func NewFileStore(filePath string) (*FileStore, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read locations file: %w", err)
	}

	var locations []Location
	if err := json.Unmarshal(data, &locations); err != nil {
		return nil, fmt.Errorf("failed to parse locations JSON: %w", err)
	}

	return &FileStore{locations: locations}, nil
}

// All returns all loaded locations.
func (s *FileStore) All() []Location {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to prevent external modification
	result := make([]Location, len(s.locations))
	copy(result, s.locations)
	return result
}

// MemoryStore holds locations in memory, useful for testing or default locations.
type MemoryStore struct {
	locations []Location
}

// NewMemoryStore creates a new MemoryStore with the given locations.
func NewMemoryStore(locations []Location) *MemoryStore {
	return &MemoryStore{locations: locations}
}

// All returns all locations.
func (s *MemoryStore) All() []Location {
	result := make([]Location, len(s.locations))
	copy(result, s.locations)
	return result
}

// DefaultLocations returns a minimal set of fallback locations.
// For full location support, use locations.json file.
func DefaultLocations() []Location {
	return []Location{
		{IATA: "LOCAL", Lat: 0, Lon: 0, CCA2: "", Region: "", City: "Local Server"},
	}
}
