package transport

const defaultProfile = "continuous-bulk-pipeline"

type appProfile struct {
	Slots             []int  `json:"slots,omitempty"`
	Rounds            int    `json:"rounds"`
	Down              int    `json:"down"`
	Duplex            bool   `json:"duplex"`
	PaintEvery        int    `json:"paint_every"`
	Streaming         any    `json:"streaming"`
	Commit            bool   `json:"commit"`
	Continuous        bool   `json:"continuous"`
	LiveDuplex        bool   `json:"live_duplex"`
	LeaseSlots        int    `json:"lease_slots"`
	Bulk              bool   `json:"bulk"`
	BulkDuplex        bool   `json:"bulk_duplex"`
	ShortState        string `json:"short_state,omitempty"`
	DeferredAck       bool   `json:"deferred_ack,omitempty"`
	BulkAckOnly       bool   `json:"bulk_ack_only,omitempty"`
	ReceiveWindow     uint32 `json:"receive_window,omitempty"`
	FillerOnly        bool   `json:"filler_only,omitempty"`
	ProgressHint      bool   `json:"progress_hint,omitempty"`
	PairBulk          bool   `json:"pair_bulk,omitempty"`
	PipelineBulk      bool   `json:"pipeline_bulk,omitempty"`
	IdleEvents        bool   `json:"idle_events,omitempty"`
	InteractiveDuplex bool   `json:"interactive_duplex,omitempty"`
}

var stagedSlots = []int{8192, 8192, 8192, 8192, 32768, 32768, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 8192, 8192}
var staged20Slots = []int{8192, 8192, 8192, 8192, 32768, 32768, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 65536, 8192, 8192}

var profiles = func() map[string]appProfile {
	base := func(rounds, down int) appProfile {
		return appProfile{Rounds: rounds, Down: down, PaintEvery: 1, Streaming: false, LeaseSlots: 4}
	}
	values := map[string]appProfile{"v1": base(16, 131072), "compact": base(16, 65536), "staged": base(18, 65536)}
	derive := func(name, parent string, change func(*appProfile)) {
		value := values[parent]
		change(&value)
		values[name] = value
	}
	derive("duplex-v1", "v1", func(p *appProfile) { p.Duplex = true })
	derive("compact-sync", "compact", func(p *appProfile) { p.Duplex = true })
	derive("compact-sync20", "compact-sync", func(p *appProfile) { p.Rounds = 20 })
	derive("compact-fast20", "compact-sync20", func(p *appProfile) { p.PaintEvery = 4 })
	derive("staged", "staged", func(p *appProfile) { p.Slots = stagedSlots })
	derive("staged-fast", "staged", func(p *appProfile) { p.PaintEvery = 2 })
	derive("staged-fast20", "staged-fast", func(p *appProfile) { p.Rounds = 20; p.Slots = staged20Slots })
	derive("staged-stream20", "staged-fast20", func(p *appProfile) { p.Streaming = true })
	derive("staged-commit20", "staged-fast20", func(p *appProfile) { p.Commit = true })
	derive("continuous-v1", "staged-fast20", func(p *appProfile) { p.Continuous = true })
	derive("continuous-sync", "continuous-v1", func(p *appProfile) { p.LiveDuplex = true })
	derive("continuous-sync2", "continuous-sync", func(p *appProfile) { p.LeaseSlots = 2 })
	derive("continuous-bulk", "continuous-v1", func(p *appProfile) { p.Bulk = true })
	values["continuous-bulk-ready"] = values["continuous-bulk"]
	derive("continuous-bulk-frames", "continuous-bulk-ready", func(p *appProfile) { p.Streaming = "frames" })
	derive("continuous-bulk-duplex", "continuous-bulk-ready", func(p *appProfile) { p.BulkDuplex = true })
	derive("continuous-bulk-interactive1", "continuous-bulk-duplex", func(p *appProfile) { p.ShortState = "interactive" })
	derive("continuous-bulk-upload1", "continuous-bulk-duplex", func(p *appProfile) { p.ShortState = "upload" })
	derive("continuous-bulk-noack", "continuous-bulk-duplex", func(p *appProfile) { p.DeferredAck = true })
	derive("continuous-bulk-noack-download", "continuous-bulk-noack", func(p *appProfile) { p.BulkAckOnly = true })
	derive("continuous-bulk-window512", "continuous-bulk-duplex", func(p *appProfile) { p.ReceiveWindow = 524288 })
	derive("continuous-bulk-filler", "continuous-bulk-duplex", func(p *appProfile) { p.FillerOnly = true })
	derive("continuous-bulk-progress", "continuous-bulk-filler", func(p *appProfile) { p.ProgressHint = true })
	derive("continuous-bulk-pair", "continuous-bulk-window512", func(p *appProfile) { p.PairBulk = true })
	derive("continuous-bulk-pipeline", "continuous-bulk-pair", func(p *appProfile) { p.PipelineBulk = true })
	derive("continuous-bulk-idle-events", "continuous-bulk-duplex", func(p *appProfile) { p.IdleEvents = true })
	derive("continuous-bulk-pipeline-events", "continuous-bulk-pipeline", func(p *appProfile) { p.IdleEvents = true })
	derive("continuous-bulk-pipeline-interactive", "continuous-bulk-pipeline", func(p *appProfile) { p.InteractiveDuplex = true })
	return values
}()

func (t *Transport) appProfile() appProfile {
	name := t.Profile
	if name == "" {
		name = defaultProfile
	}
	return profiles[name]
}
