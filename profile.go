package transport

type appProfile struct {
	Slots      []int `json:"slots,omitempty"`
	Rounds     int   `json:"rounds"`
	Down       int   `json:"down"`
	Duplex     bool  `json:"duplex"`
	PaintEvery int   `json:"paint_every"`
	Streaming  any   `json:"streaming"`
	Commit     bool  `json:"commit"`
	Continuous bool  `json:"continuous"`
	LiveDuplex bool  `json:"live_duplex"`
	LeaseSlots int   `json:"lease_slots"`
	Bulk       bool  `json:"bulk"`
}

var stagedSlots = []int{8192, 8192, 8192, 8192, 32768, 32768, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 8192, 8192}
var staged20Slots = []int{8192, 8192, 8192, 8192, 32768, 32768, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 8192, 8192}

var profiles = map[string]appProfile{
	"v1":                     {nil, 16, 131072, false, 1, false, false, false, false, 4, false},
	"duplex-v1":              {nil, 16, 131072, true, 1, false, false, false, false, 4, false},
	"compact":                {nil, 16, 65536, false, 1, false, false, false, false, 4, false},
	"compact-sync":           {nil, 16, 65536, true, 1, false, false, false, false, 4, false},
	"compact-sync20":         {nil, 20, 65536, true, 1, false, false, false, false, 4, false},
	"compact-fast20":         {nil, 20, 65536, true, 4, false, false, false, false, 4, false},
	"staged":                 {stagedSlots, 18, 65536, false, 1, false, false, false, false, 4, false},
	"staged-fast":            {stagedSlots, 18, 65536, false, 2, false, false, false, false, 4, false},
	"staged-fast20":          {staged20Slots, 20, 65536, false, 2, false, false, false, false, 4, false},
	"staged-stream20":        {staged20Slots, 20, 65536, false, 2, true, false, false, false, 4, false},
	"staged-commit20":        {staged20Slots, 20, 65536, false, 2, false, true, false, false, 4, false},
	"continuous-v1":          {staged20Slots, 20, 65536, false, 2, false, false, true, false, 4, false},
	"continuous-sync":        {staged20Slots, 20, 65536, false, 2, false, false, true, true, 4, false},
	"continuous-sync2":       {staged20Slots, 20, 65536, false, 2, false, false, true, true, 2, false},
	"continuous-bulk":        {staged20Slots, 20, 65536, false, 2, false, false, true, false, 4, true},
	"continuous-bulk-ready":  {staged20Slots, 20, 65536, false, 2, false, false, true, false, 4, true},
	"continuous-bulk-frames": {staged20Slots, 20, 65536, false, 2, "frames", false, true, false, 4, true},
}

func (t *Transport) appProfile() appProfile {
	name := t.Profile
	if name == "" {
		name = "v1"
	}
	return profiles[name]
}
