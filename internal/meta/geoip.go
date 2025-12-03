// Package meta provides client metadata extraction and lookup.
package meta

import (
	"log"
	"net"
	"net/http"

	"github.com/oschwald/geoip2-golang"
)

// GeoIPProvider looks up ASN/organization info from MaxMind GeoLite2-ASN database.
type GeoIPProvider struct {
	db         *geoip2.Reader
	hostname   string
	colo       string
	trustProxy bool
}

// NewGeoIPProvider creates a new GeoIP provider using the given database file.
// The dbPath should point to a MaxMind GeoLite2-ASN.mmdb file.
func NewGeoIPProvider(dbPath, hostname, colo string, trustProxy bool) (*GeoIPProvider, error) {
	db, err := geoip2.Open(dbPath)
	if err != nil {
		return nil, err
	}

	return &GeoIPProvider{
		db:         db,
		hostname:   hostname,
		colo:       colo,
		trustProxy: trustProxy,
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
	clientIP := ClientIPFromRequest(r, p.trustProxy)

	meta := ClientMeta{
		Hostname:     p.hostname,
		ClientIP:     clientIP,
		HTTPProtocol: HTTPProtocolFromRequest(r),
		Colo:         p.colo,
		// Defaults
		Country:    "US",
		City:       "Unknown",
		Region:     "Unknown",
		PostalCode: "",
		Latitude:   0,
		Longitude:  0,
		Timezone:   "UTC",
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
	asnDB      *geoip2.Reader
	cityDB     *geoip2.Reader
	hostname   string
	colo       string
	trustProxy bool
}

// NewCityGeoIPProvider creates a provider that uses both ASN and City databases.
// Pass empty string for cityDBPath to skip city lookups.
func NewCityGeoIPProvider(asnDBPath, cityDBPath, hostname, colo string, trustProxy bool) (*CityGeoIPProvider, error) {
	asnDB, err := geoip2.Open(asnDBPath)
	if err != nil {
		return nil, err
	}

	var cityDB *geoip2.Reader
	if cityDBPath != "" {
		cityDB, err = geoip2.Open(cityDBPath)
		if err != nil {
			asnDB.Close()
			return nil, err
		}
	}

	return &CityGeoIPProvider{
		asnDB:      asnDB,
		cityDB:     cityDB,
		hostname:   hostname,
		colo:       colo,
		trustProxy: trustProxy,
	}, nil
}

// Close closes all GeoIP databases.
func (p *CityGeoIPProvider) Close() error {
	if p.asnDB != nil {
		p.asnDB.Close()
	}
	if p.cityDB != nil {
		p.cityDB.Close()
	}
	return nil
}

// MetaFor returns metadata for the given request, including ASN and city lookup.
func (p *CityGeoIPProvider) MetaFor(r *http.Request) ClientMeta {
	clientIP := ClientIPFromRequest(r, p.trustProxy)

	meta := ClientMeta{
		Hostname:     p.hostname,
		ClientIP:     clientIP,
		HTTPProtocol: HTTPProtocolFromRequest(r),
		Colo:         p.colo,
		// Defaults
		Country:    "US",
		City:       "Unknown",
		Region:     "Unknown",
		PostalCode: "",
		Latitude:   0,
		Longitude:  0,
		Timezone:   "UTC",
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
