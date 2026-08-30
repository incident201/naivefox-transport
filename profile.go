package transport

type appProfile struct {
	Slots      []int `json:"slots,omitempty"`
	Rounds     int   `json:"rounds"`
	Down       int   `json:"down"`
	Duplex     bool  `json:"duplex"`
	PaintEvery int   `json:"paint_every"`
	Streaming  bool  `json:"streaming"`
	Commit     bool  `json:"commit"`
}

var stagedSlots = []int{8192, 8192, 8192, 8192, 32768, 32768, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 8192, 8192}
var staged20Slots = []int{8192, 8192, 8192, 8192, 32768, 32768, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 8192, 8192}

var profiles = map[string]appProfile{
	"v1":              {nil, 16, 131072, false, 1, false, false},
	"duplex-v1":       {nil, 16, 131072, true, 1, false, false},
	"compact":         {nil, 16, 65536, false, 1, false, false},
	"compact-sync":    {nil, 16, 65536, true, 1, false, false},
	"compact-sync20":  {nil, 20, 65536, true, 1, false, false},
	"compact-fast20":  {nil, 20, 65536, true, 4, false, false},
	"staged":          {stagedSlots, 18, 65536, false, 1, false, false},
	"staged-fast":     {stagedSlots, 18, 65536, false, 2, false, false},
	"staged-fast20":   {staged20Slots, 20, 65536, false, 2, false, false},
	"staged-stream20": {staged20Slots, 20, 65536, false, 2, true, false},
	"staged-commit20": {staged20Slots, 20, 65536, false, 2, false, true},
}

func (t *Transport) appProfile() appProfile {
	name := t.Profile
	if name == "" {
		name = "v1"
	}
	return profiles[name]
}
