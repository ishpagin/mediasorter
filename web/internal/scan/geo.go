package scan

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	_ "embed"
	"encoding/csv"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
)

// GeoNames cities with more than 1000 people (CC BY 4.0, geonames.org), as packaged by reverse_geocoder.
//
//go:embed cities1000.csv.gz
var citiesGz []byte

type city struct {
	x, y, z float64
	place   string
}

var (
	citiesOnce sync.Once
	cities     []city
)

// ecef: reverse_geocoder's coordinates, including its z = sin(lat in degrees) slip, so the nearest city
// is the same one the Python scanner picked.
func ecef(lat, lon float64) (float64, float64, float64) {
	const a, e2 = 6378.137, 0.00669437999014
	lr, nr := lat*math.Pi/180, lon*math.Pi/180
	n := a / math.Sqrt(1-e2*math.Pow(math.Sin(lr), 2))
	return n * math.Cos(lr) * math.Cos(nr), n * math.Cos(lr) * math.Sin(nr), n * (1 - e2) * math.Sin(lat)
}

func loadCities() {
	zr, err := gzip.NewReader(bytes.NewReader(citiesGz))
	if err != nil {
		return
	}
	r := csv.NewReader(zr)
	r.FieldsPerRecord = -1
	if _, err := r.Read(); err != nil { // header: lat,lon,name,admin1,admin2,cc
		return
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 6 {
			continue
		}
		lat, e1 := strconv.ParseFloat(rec[0], 64)
		lon, e2 := strconv.ParseFloat(rec[1], 64)
		if e1 != nil || e2 != nil {
			continue
		}
		var parts []string
		for _, p := range []string{rec[2], rec[3], rec[5]} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		x, y, z := ecef(lat, lon)
		cities = append(cities, city{x, y, z, strings.Join(parts, ", ")})
	}
}

// Place: "City, Region, CC" of the nearest city.
func Place(lat, lon float64) string {
	citiesOnce.Do(loadCities)
	x, y, z := ecef(lat, lon)
	best, bd := "", math.Inf(1)
	for i := range cities {
		c := &cities[i]
		d := (c.x-x)*(c.x-x) + (c.y-y)*(c.y-y) + (c.z-z)*(c.z-z)
		if d < bd {
			best, bd = c.place, d
		}
	}
	return best
}

// Geocode fills files.place from GPS, offline.
func Geocode(db *sql.DB) error {
	rows, err := db.Query("SELECT id, lat, lon FROM files WHERE lat IS NOT NULL AND place IS NULL")
	if err != nil {
		return err
	}
	type fix struct {
		id       int64
		lat, lon float64
	}
	var todo []fix
	for rows.Next() {
		var f fix
		if err := rows.Scan(&f.id, &f.lat, &f.lon); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, f)
	}
	rows.Close()
	for _, f := range todo {
		if _, err := db.Exec("UPDATE files SET place = ? WHERE id = ?", Place(f.lat, f.lon), f.id); err != nil {
			return err
		}
	}
	return nil
}
