// Package meta provides client metadata extraction and lookup.
package meta

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/oschwald/geoip2-golang"

	"github.com/yellowman/netspeed/internal/clientaddr"
)

// GeoIPProvider looks up ASN/organization info from MaxMind GeoLite2-ASN database.
type GeoIPProvider struct {
	db            *geoip2.Reader
	hostname      string
	colo          string
	clientAddress *clientaddr.Resolver
}

// NewGeoIPProvider creates a new GeoIP provider using the given database file.
// The dbPath should point to a MaxMind GeoLite2-ASN.mmdb file.
func NewGeoIPProvider(dbPath, hostname, colo string, clientAddress *clientaddr.Resolver) (*GeoIPProvider, error) {
	db, err := geoip2.Open(dbPath)
	if err != nil {
		return nil, err
	}

	return &GeoIPProvider{
		db:            db,
		hostname:      hostname,
		colo:          colo,
		clientAddress: clientAddress,
	}, nil
}

// Close closes the GeoIP database.
func (p *GeoIPProvider) Close() error {
	if p.db != nil {
		return p.db.Close()
	}
	return nil
}

// MetaFor returns metadata for the given request, including ASN lookup.
func (p *GeoIPProvider) MetaFor(r *http.Request) ClientMeta {
	clientIP := ClientIPFromRequest(r, p.clientAddress)

	meta := ClientMeta{
		Hostname:     p.hostname,
		ClientIP:     clientIP,
		HTTPProtocol: HTTPProtocolFromRequest(r),
		Colo:         p.colo,
		// Unknown location values remain empty until a database lookup succeeds.
		Country:    "",
		City:       "",
		Region:     "",
		PostalCode: "",
		Latitude:   0,
		Longitude:  0,
		Timezone:   "",
	}

	// Look up ASN from IP
	ip := net.ParseIP(clientIP)
	if ip == nil {
		log.Printf("GeoIP: failed to parse IP: %s", clientIP)
		return meta
	}

	asn, err := p.db.ASN(ip)
	if err != nil {
		log.Printf("GeoIP: ASN lookup failed for %s: %v", clientIP, err)
		return meta
	}

	meta.ASN = int(asn.AutonomousSystemNumber)
	meta.ASOrg = asn.AutonomousSystemOrganization

	return meta
}

// CityGeoIPProvider looks up both ASN and city/location data from MaxMind databases.
type CityGeoIPProvider struct {
	asnDB         *geoip2.Reader
	cityDB        *geoip2.Reader
	hostname      string
	colo          string
	clientAddress *clientaddr.Resolver
}

// NewCityGeoIPProvider creates a provider using either or both MaxMind ASN and
// City databases. At least one path must be non-empty.
func NewCityGeoIPProvider(asnDBPath, cityDBPath, hostname, colo string, clientAddress *clientaddr.Resolver) (*CityGeoIPProvider, error) {
	if asnDBPath == "" && cityDBPath == "" {
		return nil, fmt.Errorf("at least one GeoIP database path is required")
	}

	provider := &CityGeoIPProvider{
		hostname:      hostname,
		colo:          colo,
		clientAddress: clientAddress,
	}
	var err error
	if asnDBPath != "" {
		provider.asnDB, err = geoip2.Open(asnDBPath)
		if err != nil {
			return nil, fmt.Errorf("open ASN database: %w", err)
		}
	}
	if cityDBPath != "" {
		provider.cityDB, err = geoip2.Open(cityDBPath)
		if err != nil {
			if provider.asnDB != nil {
				_ = provider.asnDB.Close()
			}
			return nil, fmt.Errorf("open City database: %w", err)
		}
	}
	return provider, nil
}

// Close closes all GeoIP databases and returns any close failures.
func (p *CityGeoIPProvider) Close() error {
	var closeErrors []error
	if p.asnDB != nil {
		if err := p.asnDB.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if p.cityDB != nil {
		if err := p.cityDB.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

// MetaFor returns metadata for the given request, including ASN and city lookup.
func (p *CityGeoIPProvider) MetaFor(r *http.Request) ClientMeta {
	clientIP := ClientIPFromRequest(r, p.clientAddress)

	meta := ClientMeta{
		Hostname:     p.hostname,
		ClientIP:     clientIP,
		HTTPProtocol: HTTPProtocolFromRequest(r),
		Colo:         p.colo,
		// Unknown location values remain empty until a database lookup succeeds.
		Country:    "",
		City:       "",
		Region:     "",
		PostalCode: "",
		Latitude:   0,
		Longitude:  0,
		Timezone:   "",
	}

	ip := net.ParseIP(clientIP)
	if ip == nil {
		log.Printf("GeoIP: failed to parse IP: %s", clientIP)
		return meta
	}

	// Look up ASN
	if p.asnDB != nil {
		asn, err := p.asnDB.ASN(ip)
		if err == nil {
			meta.ASN = int(asn.AutonomousSystemNumber)
			meta.ASOrg = asn.AutonomousSystemOrganization
		} else {
			log.Printf("GeoIP: ASN lookup failed for %s: %v", clientIP, err)
		}
	}

	// Look up city/location
	if p.cityDB != nil {
		city, err := p.cityDB.City(ip)
		if err == nil {
			if city.Country.IsoCode != "" {
				meta.Country = city.Country.IsoCode
			}
			if city.City.Names != nil {
				if name, ok := city.City.Names["en"]; ok {
					meta.City = name
				}
			}
			if len(city.Subdivisions) > 0 && city.Subdivisions[0].Names != nil {
				if name, ok := city.Subdivisions[0].Names["en"]; ok {
					meta.Region = name
				}
			}
			if city.Postal.Code != "" {
				meta.PostalCode = city.Postal.Code
			}
			meta.Latitude = city.Location.Latitude
			meta.Longitude = city.Location.Longitude
			if city.Location.TimeZone != "" {
				meta.Timezone = city.Location.TimeZone
			}
		} else {
			log.Printf("GeoIP: city lookup failed for %s: %v", clientIP, err)
		}
	}

	return meta
}
