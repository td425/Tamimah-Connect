package dialer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/td425/tpbx/internal/ari"
	"github.com/td425/tpbx/internal/store"
)

// Engine runs the outbound dialer: one loop that, every tick, tops up each
// running campaign's hopper, asks the pacing rules how many lines to open, and
// opens them.
//
// Calls are originated **into this Stasis application** rather than into the
// dialplan, so the engine owns each channel from the moment it answers. That
// ownership is the point: only the engine knows whether an agent is free right
// now, and that decision cannot be delegated to a dialplan.
//
// Scale: the engine runs in-process, as goroutines alongside the ARI and AMI
// loops. That is sized for the single-VM deployment this product targets
// (roughly a hundred agents). The seam for moving it out is this type — it
// talks to Postgres and ARI and to nothing else in the binary — but nothing
// here assumes it is alone: the hopper claim (FOR UPDATE SKIP LOCKED) is
// already safe against a second dialer, which is what would break first.
type Engine struct {
	ARI    *ari.Client
	Work   *store.AgentWork
	Hopper *store.Hopper
	Leads  *store.Leads
	Calls  *store.LeadCalls

	// Tick is how often pacing runs. Shorter reacts faster and costs more
	// queries; two seconds is well inside a human's tolerance for being
	// connected and cheap enough to run all day.
	Tick time.Duration

	// RateWindow is how far back the answer and drop rates are measured. Long
	// enough to be a real sample, short enough that a campaign which changes
	// behaviour is not paced by an hour-old picture.
	RateWindow time.Duration

	mu sync.Mutex
	// inFlight counts lines opened but not yet resolved, per campaign. Held in
	// memory because it must be exact at the moment of the pacing decision; the
	// database copy (calls with no ended_at) is the durable version and is what
	// a restart recovers from.
	inFlight map[int64]int
	// calls maps an ARI channel to what the engine knows about it, so a Stasis
	// event can be traced back to the lead that caused it.
	calls map[string]*outbound
	// agentLegs maps an agent to their parked channel and bridge.
	agentLegs map[int64]*agentLeg
	// warned remembers which campaigns have already had a misconfiguration
	// logged, so a broken campaign does not fill the log at tick speed.
	warned map[int64]bool
}

// warnOnce logs a campaign misconfiguration the first time it is seen.
func (e *Engine) warnOnce(campaignID int64, msg, code string) {
	e.mu.Lock()
	seen := e.warned[campaignID]
	e.warned[campaignID] = true
	e.mu.Unlock()
	if !seen {
		slog.Warn(msg, "campaign", code)
	}
}

// outbound is one call the engine placed.
type outbound struct {
	CampaignID int64
	LeadID     int64
	HopperID   int64
	CallID     int64
	Number     string
	Placed     time.Time
}

// agentLeg is an agent's own channel, held up in a bridge so a customer can be
// moved into it without the agent's phone ringing.
type agentLeg struct {
	AgentID   int64
	ChannelID string
	BridgeID  string
	Busy      bool
}

// New returns an Engine with sensible defaults.
func New(a *ari.Client, work *store.AgentWork, hopper *store.Hopper, leads *store.Leads, calls *store.LeadCalls) *Engine {
	return &Engine{
		ARI:        a,
		Work:       work,
		Hopper:     hopper,
		Leads:      leads,
		Calls:      calls,
		Tick:       2 * time.Second,
		RateWindow: 30 * time.Minute,
		inFlight:   map[int64]int{},
		calls:      map[string]*outbound{},
		agentLegs:  map[int64]*agentLeg{},
		warned:     map[int64]bool{},
	}
}

// Run drives the dialer until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	slog.Info("dialer engine started", "tick", e.Tick)
	ticker := time.NewTicker(e.Tick)
	defer ticker.Stop()

	// A separate, slower sweep for housekeeping that must not run at pacing
	// speed: releasing hopper entries whose dial never completed.
	sweep := time.NewTicker(time.Minute)
	defer sweep.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("dialer engine stopping")
			e.releaseAll(context.Background())
			return
		case <-ticker.C:
			e.tick(ctx)
		case <-sweep.C:
			if n, err := e.Hopper.ReleaseStale(ctx, 5*time.Minute); err == nil && n > 0 {
				slog.Warn("released stale hopper entries", "count", n)
			}
		}
	}
}

// tick runs one pacing round across every running campaign.
func (e *Engine) tick(ctx context.Context) {
	campaigns, err := e.Work.RunningCampaigns(ctx)
	if err != nil {
		slog.Warn("dialer: list running campaigns", "err", err)
		return
	}
	for _, c := range campaigns {
		if !Automatic(c.DialMethod) {
			// The campaign's dialer is on but its method is agent-driven.
			// Nothing to do — agents dial it themselves.
			continue
		}
		e.runCampaign(ctx, c)
	}
}

func (e *Engine) runCampaign(ctx context.Context, c store.RunningCampaign) {
	// An automatic call has no agent to pick a route for it, so the campaign
	// must name the trunk its calls leave by. Without one the originate would
	// fail on every tick; complain once per campaign instead.
	if c.Trunk == "" {
		e.warnOnce(c.ID, "dialer: campaign has no trunk set, cannot dial automatically", c.Code)
		return
	}

	// Keep the queue stocked. Failing to fill is not fatal: the loop simply
	// finds nothing to dial and tries again next tick.
	depth, err := e.Hopper.Depth(ctx, c.ID)
	if err != nil {
		slog.Warn("dialer: hopper depth", "campaign", c.Code, "err", err)
		return
	}
	if depth < c.HopperLevel {
		if _, err := e.Hopper.Fill(ctx, c.ID, c.HopperLevel-depth); err != nil {
			slog.Warn("dialer: hopper fill", "campaign", c.Code, "err", err)
		}
	}

	ready, err := e.Work.ReadyAgents(ctx, c.ID)
	if err != nil {
		slog.Warn("dialer: ready agents", "campaign", c.Code, "err", err)
		return
	}
	recent, err := e.Work.RecentCalls(ctx, c.ID, e.RateWindow)
	if err != nil {
		slog.Warn("dialer: recent calls", "campaign", c.Code, "err", err)
		return
	}

	answerRate := 0.0
	if recent.Placed > 0 {
		answerRate = float64(recent.Answered) / float64(recent.Placed)
	}

	e.mu.Lock()
	inFlight := e.inFlight[c.ID]
	e.mu.Unlock()

	lines := LinesToOpen(Pace{
		Method:          c.DialMethod,
		DialLevel:       c.DialLevel,
		AdaptiveMax:     c.AdaptiveMax,
		AvailableAgents: len(ready),
		LinesInFlight:   inFlight,
		AnswerRate:      answerRate,
		DropRate:        DropRatePercent(recent.Answered, recent.Dropped),
		DropCeiling:     c.DropCeiling,
		SampleSize:      recent.Placed,
	})
	if lines <= 0 {
		return
	}

	// Make sure the agents who will take these calls are already up and
	// waiting, so an answer can be connected without a ring.
	for _, a := range ready {
		e.ensureAgentLeg(ctx, a)
	}

	for i := 0; i < lines; i++ {
		if !e.placeCall(ctx, c) {
			break // nothing left in the hopper, or dialing is failing
		}
	}
}

// placeCall takes one lead from the hopper and dials it. It reports whether it
// managed to place a call, so the caller stops trying when the queue is empty.
func (e *Engine) placeCall(ctx context.Context, c store.RunningCampaign) bool {
	entry, err := e.Hopper.Take(ctx, c.ID)
	if err != nil {
		return false // empty hopper is the normal case here
	}

	lead, err := e.Leads.Get(ctx, entry.LeadID)
	if err != nil {
		_ = e.Hopper.Done(ctx, entry.ID)
		return true
	}

	number, err := store.CheckPhoneNumber(lead.PhoneCode + lead.PhoneNumber)
	if err != nil {
		slog.Warn("dialer: unusable number, skipping lead", "lead", lead.ID, "err", err)
		_ = e.Hopper.Done(ctx, entry.ID)
		return true
	}

	// Check the suppression list again, here, immediately before dialing. The
	// hopper filler already checked — but a number can be added to the list in
	// the seconds since, and this is the check that decides whether a phone
	// actually rings.
	if blocked, err := e.Hopper.IsDNC(ctx, number, c.ID); err == nil && blocked {
		slog.Info("dialer: lead is on the do-not-call list, skipping", "lead", lead.ID)
		_ = e.Hopper.Done(ctx, entry.ID)
		return true
	}

	campaignID := c.ID
	call, err := e.Calls.Start(ctx, store.LeadCall{
		LeadID:     lead.ID,
		CampaignID: &campaignID,
		Direction:  "out",
		Dialed:     number,
		PlacedBy:   "auto",
	})
	if err != nil {
		slog.Warn("dialer: could not record call", "lead", lead.ID, "err", err)
		_ = e.Hopper.Release(ctx, entry.ID)
		return false
	}

	ch, err := e.ARI.OriginateToApp(ctx,
		"PJSIP/"+number+"@"+trunkFor(c),
		c.OutboundCID,
		strconv.Itoa(c.DialTimeout),
		map[string]string{
			"TPBX_LEAD":     strconv.FormatInt(lead.ID, 10),
			"TPBX_CAMPAIGN": strconv.FormatInt(c.ID, 10),
			"TPBX_CALL":     strconv.FormatInt(call.ID, 10),
		})
	if err != nil {
		slog.Warn("dialer: originate failed", "number", number, "err", err)
		_ = e.Calls.Finish(ctx, call.ID, store.CallOutcome{Status: "FAILED"})
		_ = e.Hopper.Done(ctx, entry.ID)
		return false
	}

	e.mu.Lock()
	e.inFlight[c.ID]++
	e.calls[ch.ID] = &outbound{
		CampaignID: c.ID, LeadID: lead.ID, HopperID: entry.ID,
		CallID: call.ID, Number: number, Placed: time.Now(),
	}
	e.mu.Unlock()
	return true
}

// trunkFor picks the endpoint a campaign's calls leave by. An empty trunk means
// "use the outbound routes", which for a Stasis originate means dialing through
// the campaign's configured trunk is required — so a campaign with no trunk set
// cannot dial automatically, and the console says so.
func trunkFor(c store.RunningCampaign) string {
	return c.Trunk
}

// HandleEvent feeds one ARI event to the engine. main passes every Stasis event
// here as well as to the browser hub; events for channels the engine did not
// place are ignored.
func (e *Engine) HandleEvent(ctx context.Context, ev ari.Event) {
	switch ev.Type {
	case "StasisStart":
		e.onAnswer(ctx, channelID(ev))
	case "StasisEnd", "ChannelDestroyed", "ChannelHangupRequest":
		e.onEnd(ctx, channelID(ev))
	}
}

// onAnswer handles a customer picking up. Everything the engine exists to do
// happens in the next few hundred milliseconds: decide whether there is an
// agent, and either connect the call or abandon it honestly.
func (e *Engine) onAnswer(ctx context.Context, chID string) {
	if chID == "" {
		return
	}
	e.mu.Lock()
	ob, ours := e.calls[chID]
	e.mu.Unlock()
	if !ours {
		return // an agent leg or someone else's channel
	}

	if err := e.ARI.Answer(ctx, chID); err != nil {
		slog.Warn("dialer: answer failed", "channel", chID, "err", err)
	}

	agent := e.claimAgent(ctx, ob.CampaignID)
	if agent == nil {
		// Nobody free. This is an abandoned call: say so out loud, record it,
		// and let the pacing governor see it. Hiding it would leave the drop
		// rate looking better than the campaign is actually behaving.
		e.abandon(ctx, chID, ob)
		return
	}

	if err := e.ARI.AddToBridge(ctx, agent.BridgeID, chID); err != nil {
		slog.Warn("dialer: could not connect call to agent", "agent", agent.AgentID, "err", err)
		e.releaseAgent(agent.AgentID)
		e.abandon(ctx, chID, ob)
		return
	}

	if err := e.Calls.MarkAnswered(ctx, ob.CallID, agent.AgentID); err != nil {
		slog.Warn("dialer: could not record answer", "call", ob.CallID, "err", err)
	}
	slog.Info("dialer: connected", "lead", ob.LeadID, "agent", agent.AgentID,
		"waited", time.Since(ob.Placed).Round(time.Millisecond))
}

// abandon plays the safe-harbour message and hangs up, recording the call as
// dropped.
func (e *Engine) abandon(ctx context.Context, chID string, ob *outbound) {
	// Best-effort: the message is a courtesy and a legal nicety, but the
	// recording of the drop is not optional and happens either way.
	if _, err := e.ARI.Play(ctx, chID, safeHarbourMedia); err != nil {
		slog.Debug("dialer: safe-harbour message failed", "err", err)
	}
	time.Sleep(safeHarbourWait)
	_ = e.ARI.Hangup(ctx, chID, "normal")

	if err := e.Calls.Finish(ctx, ob.CallID, store.CallOutcome{Status: "DROP", Dropped: true}); err != nil {
		slog.Warn("dialer: could not record drop", "call", ob.CallID, "err", err)
	}
	slog.Warn("dialer: abandoned call, no agent available", "lead", ob.LeadID, "campaign", ob.CampaignID)
}

// safeHarbourMedia is what an abandoned call hears. Regulations in several
// jurisdictions require a recorded identification rather than silence or a
// hangup; the file is provisioned with the other prompts.
const safeHarbourMedia = "sound:tpbx/safe-harbour"

// safeHarbourWait is how long the message is given before the line is cleared.
const safeHarbourWait = 2 * time.Second

// onEnd closes out a call the engine placed.
func (e *Engine) onEnd(ctx context.Context, chID string) {
	if chID == "" {
		return
	}
	e.mu.Lock()
	ob, ours := e.calls[chID]
	if ours {
		delete(e.calls, chID)
		if e.inFlight[ob.CampaignID] > 0 {
			e.inFlight[ob.CampaignID]--
		}
	}
	// An agent's own leg going away means they are no longer parked.
	for id, leg := range e.agentLegs {
		if leg.ChannelID == chID {
			delete(e.agentLegs, id)
		}
	}
	e.mu.Unlock()

	if !ours {
		return
	}
	_ = e.Hopper.Done(ctx, ob.HopperID)
	// A call that ends without ever being answered rang out; the disposition
	// belongs to the agent when there was one, so only the unanswered case is
	// closed here.
	if err := e.Calls.FinishIfOpen(ctx, ob.CallID, "NA"); err != nil {
		slog.Debug("dialer: finish call", "call", ob.CallID, "err", err)
	}
}

// claimAgent reserves the longest-idle ready agent on a campaign and returns
// their parked leg, or nil when nobody is free.
func (e *Engine) claimAgent(ctx context.Context, campaignID int64) *agentLeg {
	ready, err := e.Work.ReadyAgents(ctx, campaignID)
	if err != nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range ready {
		leg, ok := e.agentLegs[a.AgentID]
		if !ok || leg.Busy {
			continue
		}
		leg.Busy = true
		return leg
	}
	return nil
}

func (e *Engine) releaseAgent(agentID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if leg, ok := e.agentLegs[agentID]; ok {
		leg.Busy = false
	}
}

// ensureAgentLeg makes sure a ready agent has their own channel up and parked in
// a bridge, so a connect is a bridge move rather than a new call.
//
// This is what a predictive connect costs: the agent's phone is in a call for
// their whole shift. That is the standard model for a predictive desk and the
// only way to avoid making the customer listen to the agent's phone ring after
// they have already said hello.
func (e *Engine) ensureAgentLeg(ctx context.Context, a store.ReadyAgent) {
	e.mu.Lock()
	if _, ok := e.agentLegs[a.AgentID]; ok {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	bridgeID := fmt.Sprintf("tpbx-agent-%d", a.AgentID)
	if _, err := e.ARI.CreateBridge(ctx, bridgeID); err != nil {
		slog.Warn("dialer: create agent bridge", "agent", a.AgentID, "err", err)
		return
	}
	ch, err := e.ARI.OriginateToApp(ctx, "PJSIP/"+a.Extension, "", "30",
		map[string]string{"TPBX_AGENT": strconv.FormatInt(a.AgentID, 10)})
	if err != nil {
		slog.Warn("dialer: could not call agent", "agent", a.AgentID, "err", err)
		return
	}
	if err := e.ARI.AddToBridge(ctx, bridgeID, ch.ID); err != nil {
		slog.Warn("dialer: could not park agent", "agent", a.AgentID, "err", err)
		_ = e.ARI.Hangup(ctx, ch.ID, "normal")
		return
	}

	e.mu.Lock()
	e.agentLegs[a.AgentID] = &agentLeg{AgentID: a.AgentID, ChannelID: ch.ID, BridgeID: bridgeID}
	e.mu.Unlock()
	slog.Info("dialer: agent parked and ready", "agent", a.AgentID, "extension", a.Extension)
}

// releaseAll tears down every parked agent leg on shutdown. Leaving them up
// would strand agents in a call with a dialer that is no longer running.
func (e *Engine) releaseAll(ctx context.Context) {
	e.mu.Lock()
	legs := make([]*agentLeg, 0, len(e.agentLegs))
	for _, l := range e.agentLegs {
		legs = append(legs, l)
	}
	e.agentLegs = map[int64]*agentLeg{}
	e.mu.Unlock()

	for _, l := range legs {
		_ = e.ARI.Hangup(ctx, l.ChannelID, "normal")
		_ = e.ARI.DestroyBridge(ctx, l.BridgeID)
	}
}

// channelID digs the channel id out of a raw ARI event. Events arrive as raw
// JSON so the hub can forward them verbatim to browsers; the engine only needs
// the one field.
func channelID(ev ari.Event) string {
	var payload struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(ev.Raw, &payload); err != nil {
		return ""
	}
	return payload.Channel.ID
}
