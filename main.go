// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// battery-model reads CSV files and uses historical data to
// model scenarios of no-solar, solar only, and solar with battery.
// The CSV files are assumed to be in a separate directory.
// A directory walk is used to read the CSV files, which should
// be in time order e.g named as yyyy-mm-dd
// The first line of each file is assumed to be a commented header line e.g
//
//    #date,time,EXP,IMP,GEN-T,...
//
// This header line is used to identify the columns to be used.
//
// The relevant column titles that are processed are:
// date - to get the date
// time - time
// IMP - Accumlating imported energy (kWh)
// EXP - Accumlating exported energy (kWh)
// GEN-T - Accumlating solar generation (kWh)
//
// The MeterMan project generates CSV files of this format.

package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

var baseDir = flag.String("dir", "/var/lib/MeterMan/csv", "Base directory for CSV files")
var configFile = flag.String("config", "costs.yml", "YAML config file")
var interval = flag.Int("interval", 10, "Max interval betweeen samples")

// Format for parsing combined date/time
const tFmt = "2006-01-02 15:04"

// CSV column headers
const h_date = "#date"
const h_time = "time"
const h_import = "IMP"
const h_export = "EXP"
const h_gen = "GEN-T"

type Battery struct {
	Size      float64 `yaml:"size"`
	Recharge  float64 `yaml:"recharge"`
	Discharge float64 `yaml:"discharge"`
}

type Cost struct {
	Start  string
	Daily  float64
	Kwh    float64
	FeedIn float64 `yaml:"feed_in"`
}

type Config struct {
	Battery Battery
	Years   []int
	Cost    []Cost
}

// Accumlators tracking
type stat struct {
	last  float64 // Prior sample value (to detect resets)
	value float64 // Current interval's value
}

// totals holds the accumulated totals
type totals struct {
	cost float64 // Cost in cents
	imp  float64 // Imported power in kWh
	exp  float64 // Exported power in kWh
}

var config Config

var nosolar, solar, solarBattery totals

var battery float64 // Running value of battery charge
var lastTime time.Time
var chargeTotal float64      // Total of power used to charge battery (kWh)
var consumptionTotal float64 // Total power consumed (kWh)
var batteryTotal float64     // Total battery power discharged (kWh)
var imp, exp, gen stat       // Running values of import, export and generated power (kWh)
var ndays int                // Number of days processed

func main() {
	flag.Parse()

	conf, err := ioutil.ReadFile(*configFile)
	if err != nil {
		log.Fatalf("Can't read config %s: %v", *configFile, err)
	}
	if err := yaml.Unmarshal(conf, &config); err != nil {
		log.Fatalf("Can't parse config %s: %v", *configFile, err)
	}
	files, err := getFileNames(*baseDir, config.Years)
	if err != nil {
		log.Fatalf("%s: %v", *baseDir, err)
	}
	// Assume battery is already charged
	battery = config.Battery.Discharge
	// Iterate through all the files in time order, and read the CSV data.
	for _, f := range files {
		err := readCSV(f)
		if err != nil {
			log.Printf("%s: %v\n", f, err)
			continue
		}
	}
	ny := float64(ndays) / 365.25 // Number of years
	fmt.Printf("Days: %d, years: %.1f\n", ndays, ny)
	// Convert to dollars
	nosolar.cost /= 100.0
	solar.cost /= 100.0
	solarBattery.cost /= 100.0
	fmt.Printf("              | Total cost |  Cost PA  |  Import  |  Export  |\n")
	printTotal("No solar", nosolar)
	printTotal("Solar", solar)
	printTotal("Solar+battery", solarBattery)
	fmt.Printf("Total consumption: %.0fkWh, battery charging %.0fkWh, battery discharge %0.fkWh\n", consumptionTotal, chargeTotal, batteryTotal)
	// Show differences
	diff := nosolar.cost - solar.cost
	fmt.Printf("Between no-solar/solar: total $%.2f, per day: $%.2f, per year: $%.2f\n", diff, diff/float64(ndays), diff/ny)
	diff = nosolar.cost - solarBattery.cost
	fmt.Printf("Between no-solar/solar+battery: total $%.2f, per day: $%.2f, per year: $%.2f\n", diff, diff/float64(ndays), diff/ny)
	diff = solar.cost - solarBattery.cost
	fmt.Printf("Between solar/solar+battery: total $%.2f, per day: $%.2f, per year: $%.2f\n", diff, diff/float64(ndays), diff/ny)
}

// printTotal prints the totals for the scenario
func printTotal(title string, t totals) {
	ny := float64(ndays) / 365.25 // Number of years
	c := fmt.Sprintf("$%.2f", t.cost)
	pa := fmt.Sprintf("$%.2f", t.cost/ny)
	fmt.Printf("%-14s| %10s | %9s | %8.0f | %8.0f |\n", title, c, pa, t.imp, t.exp)
}

// getFileNames walks the directory and returns the files in sorted order.
func getFileNames(base string, years []int) ([]string, error) {
	var files []string

	for _, dir := range years {
		err := filepath.Walk(filepath.Join(base, fmt.Sprintf("%d", dir)),
			func(path string, info os.FileInfo, err error) error {
				if (info.Mode() & os.ModeType) == 0 {
					files = append(files, path)
				}
				return err
			})
		if err != nil {
			return files, err
		}
	}
	sort.Strings(files)
	return files, nil
}

// readCSV reads one CSV file and extracts the samples
func readCSV(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	rdr := csv.NewReader(f)
	// Allow variable number of fields
	rdr.FieldsPerRecord = -1
	r, err := rdr.ReadAll()
	if err != nil {
		return err
	}
	// File must contain at least a header line and one line of data
	if len(r) < 2 {
		log.Printf("%s: empty file", file)
		return nil
	}
	// Find columns in header line
	dateCol := -1
	timeCol := -1
	impCol := -1
	expCol := -1
	genCol := -1
	for i, s := range r[0] {
		switch s {
		case h_date:
			dateCol = i
			break

		case h_time:
			timeCol = i
			break

		case h_import:
			impCol = i
			break

		case h_export:
			expCol = i
			break

		case h_gen:
			genCol = i
			break
		}
	}
	if dateCol == -1 || timeCol == -1 || impCol == -1 || expCol == -1 || genCol == -1 {
		log.Printf("%s: Not all required fields are present", file)
		return nil
	}
	// valid CSV fields are present
	ndays++
	// TODO: Lookup the cost
	costIndex := 0
	// Iterate through the records
	for i, data := range r[1:] {
		var err error

		if len(data) < len(r[0]) {
			log.Printf("%s: %d: Mismatch in column count", file, i+1)
			continue
		}
		t := data[dateCol] + " " + data[timeCol]
		tm, err := time.ParseInLocation(tFmt, t, time.Local)
		if err != nil {
			log.Printf("%s: %d: Cannot parse date (%s)", file, i+1, t)
			continue
		}
		imp.UpdateString(data[impCol])
		exp.UpdateString(data[expCol])
		gen.UpdateString(data[genCol])
		if lastTime.IsZero() {
			// Skip first entry
			lastTime = tm
			continue
		}
		intv := tm.Sub(lastTime)
		lastTime = tm
		if intv > (time.Minute * time.Duration(*interval)) {
			log.Printf("Skipping interval of %s before %s", intv.String(), tm.String())
			imp.Reset()
			exp.Reset()
			gen.Reset()
			continue
		}
		consumption := imp.value + gen.value - exp.value
		consumptionTotal += consumption
		// Calculate no solar value
		nosolar.imp += consumption
		nosolar.cost += consumption * config.Cost[costIndex].Kwh
		// Calculate values without battery
		solar.imp += imp.value
		solar.exp += exp.value
		solar.cost += imp.value*config.Cost[costIndex].Kwh - exp.value*config.Cost[costIndex].FeedIn
		// Cost with a battery.
		// If any power imported, work out what the battery could have supplied in that interval
		bCap := config.Battery.Discharge * float64(intv) / float64(time.Hour) // Max energy that battery can supply in this interval
		if imp.value > 0 {
			bImp := imp.value
			bUsed := bCap
			// Don't exceed the battery capacity
			if bUsed > battery {
				bUsed = battery
			}
			if bImp > bUsed {
				// Battery replaces only part of import
				bImp -= bUsed
			} else {
				// Battery supplies all power
				bUsed = bImp
				bImp = 0
			}
			// Adjust current battery charge
			battery -= bUsed
			batteryTotal += bUsed
			solarBattery.imp += bImp
			solarBattery.cost += bImp * config.Cost[costIndex].Kwh
		}
		// If any power exported, then apply it to battery charging instead of feedin.
		// Battery charging isn't 1:1, there is a recharge efficiency i.e
		// At 90%, it takes 10/9 kWh to charge 1 kWh
		if exp.value > 0 {
			// power required to charge battery
			bChg := (config.Battery.Size - battery) / (config.Battery.Recharge / 100.0)
			// Cap to the max charging rate of the battery.
			if bChg > bCap {
				bChg = bCap
			}
			expFeed := exp.value
			// Power left over from charging can be exported.
			if expFeed > bChg {
				expFeed -= bChg
			} else {
				bChg = expFeed
				expFeed = 0
			}
			battery += bChg * (config.Battery.Recharge / 100.0)
			chargeTotal += bChg
			if battery > config.Battery.Size {
				log.Printf("Overcharge!: %f", battery)
			}
			solarBattery.exp += expFeed
			solarBattery.cost -= expFeed * config.Cost[costIndex].FeedIn
		}
	}
	// Add the daily supply charge
	solar.cost += config.Cost[costIndex].Daily
	nosolar.cost += config.Cost[costIndex].Daily
	solarBattery.cost += config.Cost[costIndex].Daily
	return nil
}

// UpdateString will convert the string and update the value
func (s *stat) UpdateString(str string) {
	val, err := strconv.ParseFloat(str, 64)
	if err == nil && val != 0 {
		s.Update(val)
	}
}

// Update will update the stat's value
func (s *stat) Update(v float64) {
	if s.last == 0 || v < s.last {
		// Reset base if first item or value has gone backwards
		s.last = v
	}
	s.value = v - s.last
	s.last = v
}

// Reset will reset the current interval value
func (s *stat) Reset() {
	s.last = 0
}
