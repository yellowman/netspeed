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

// DefaultLocations returns a set of default sample locations.
func DefaultLocations() []Location {
	return []Location{
		{
			IATA:   "Bend",
			Lat:    44.0582,
			Lon:    -121.3153,
			CCA2:   "US",
			Region: "North America",
			City:   "Bend",
		},
		{
			IATA:   "RDM",
			Lat:    44.2541,
			Lon:    -121.1500,
			CCA2:   "US",
			Region: "North America",
			City:   "Redmond",
		},
		{
			IATA:   "JFK",
			Lat:    40.6413,
			Lon:    -73.7781,
			CCA2:   "US",
			Region: "North America",
			City:   "New York",
		},
		{
			IATA:   "LAX",
			Lat:    33.9425,
			Lon:    -118.4081,
			CCA2:   "US",
			Region: "North America",
			City:   "Los Angeles",
		},
		{
			IATA:   "ORD",
			Lat:    41.9742,
			Lon:    -87.9073,
			CCA2:   "US",
			Region: "North America",
			City:   "Chicago",
		},
		{
			IATA:   "LHR",
			Lat:    51.4700,
			Lon:    -0.4543,
			CCA2:   "GB",
			Region: "Europe",
			City:   "London",
		},
		{
			IATA:   "FRA",
			Lat:    50.0379,
			Lon:    8.5622,
			CCA2:   "DE",
			Region: "Europe",
			City:   "Frankfurt",
		},
		{
			IATA:   "CDG",
			Lat:    49.0097,
			Lon:    2.5479,
			CCA2:   "FR",
			Region: "Europe",
			City:   "Paris",
		},
		{
			IATA:   "NRT",
			Lat:    35.7720,
			Lon:    140.3929,
			CCA2:   "JP",
			Region: "Asia",
			City:   "Tokyo",
		},
		{
			IATA:   "SIN",
			Lat:    1.3644,
			Lon:    103.9915,
			CCA2:   "SG",
			Region: "Asia",
			City:   "Singapore",
		},
		{
			IATA:   "SYD",
			Lat:    -33.9399,
			Lon:    151.1753,
			CCA2:   "AU",
			Region: "Oceania",
			City:   "Sydney",
		},
		{
			IATA:   "GRU",
			Lat:    -23.4356,
			Lon:    -46.4731,
			CCA2:   "BR",
			Region: "South America",
			City:   "Sao Paulo",
		},
	}
}
