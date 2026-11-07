package cast

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Device is a Cast device found on the local network.
type Device struct {
	ID    string // stable id from the device's TXT record
	Name  string // the name the owner gave it, such as "Living Room TV"
	Model string // such as "SHIELD Android TV"
	Addr  netip.AddrPort
}

const castService = "_googlecast._tcp.local."

var mdnsGroup = netip.MustParseAddrPort("224.0.0.251:5353")

// Discover asks the local network for Cast devices and collects the answers
// until ctx ends. Devices answer within a second or two.
func Discover(ctx context.Context) ([]Device, error) {
	conn, err := net.ListenMulticastUDP("udp4", nil, net.UDPAddrFromAddrPort(mdnsGroup))
	if err != nil {
		return nil, fmt.Errorf("cast: mdns listen: %w", err)
	}
	defer conn.Close()

	query, err := mdnsQuery()
	if err != nil {
		return nil, err
	}
	// Ask twice: a device that misses the first query answers the second.
	send := func() { _, _ = conn.WriteToUDPAddrPort(query, mdnsGroup) }
	send()
	go func() {
		select {
		case <-time.After(500 * time.Millisecond):
			send()
		case <-ctx.Done():
		}
	}()

	r := newResolver()
	buf := make([]byte, 9000)
	for {
		deadline, ok := ctx.Deadline()
		if !ok {
			deadline = time.Now().Add(2 * time.Second)
		}
		_ = conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil || isTimeout(err) {
				return r.devices(), nil
			}
			return r.devices(), fmt.Errorf("cast: mdns read: %w", err)
		}
		r.add(buf[:n])
	}
}

func mdnsQuery() ([]byte, error) {
	name, err := dnsmessage.NewName(castService)
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return b.Finish()
}

// resolver joins the records of all answers: PTR names an instance, its SRV
// gives host and port, its TXT the name and model, and the host's A record
// the address. A device can send them in separate packets.
type resolver struct {
	instances map[string]bool
	srv       map[string]dnsmessage.SRVResource
	txt       map[string][]string
	addrs     map[string]netip.Addr
}

func newResolver() *resolver {
	return &resolver{
		instances: map[string]bool{},
		srv:       map[string]dnsmessage.SRVResource{},
		txt:       map[string][]string{},
		addrs:     map[string]netip.Addr{},
	}
}

func (r *resolver) add(packet []byte) {
	var p dnsmessage.Parser
	if _, err := p.Start(packet); err != nil {
		return
	}
	if err := p.SkipAllQuestions(); err != nil {
		return
	}
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			break
		}
		r.record(&p, h, p.SkipAnswer)
	}
	if err := p.SkipAllAuthorities(); err != nil {
		return
	}
	for {
		h, err := p.AdditionalHeader()
		if err != nil {
			break
		}
		r.record(&p, h, p.SkipAdditional)
	}
}

// record reads one resource of h; skip passes over a resource it does not
// need, which the parser does per section.
func (r *resolver) record(p *dnsmessage.Parser, h dnsmessage.ResourceHeader, skip func() error) {
	name := strings.ToLower(h.Name.String())
	switch h.Type {
	case dnsmessage.TypePTR:
		ptr, err := p.PTRResource()
		if err == nil && name == castService {
			r.instances[strings.ToLower(ptr.PTR.String())] = true
		}
	case dnsmessage.TypeSRV:
		if srv, err := p.SRVResource(); err == nil {
			r.srv[name] = srv
		}
	case dnsmessage.TypeTXT:
		if txt, err := p.TXTResource(); err == nil {
			r.txt[name] = txt.TXT
		}
	case dnsmessage.TypeA:
		if a, err := p.AResource(); err == nil {
			r.addrs[name] = netip.AddrFrom4(a.A)
		}
	default:
		_ = skip()
	}
}

// devices lists the instances whose address is known, sorted by name.
func (r *resolver) devices() []Device {
	var out []Device
	for instance := range r.instances {
		srv, ok := r.srv[instance]
		if !ok {
			continue
		}
		addr, ok := r.addrs[strings.ToLower(srv.Target.String())]
		if !ok {
			continue
		}
		d := Device{Addr: netip.AddrPortFrom(addr, srv.Port)}
		for _, kv := range r.txt[instance] {
			key, value, _ := strings.Cut(kv, "=")
			switch key {
			case "id":
				d.ID = value
			case "fn":
				d.Name = value
			case "md":
				d.Model = value
			}
		}
		if d.Name == "" {
			d.Name = strings.TrimSuffix(instance, "."+castService)
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}
