package dns

import (
	"github.com/flyqie/memobird-priv/internal/pkg/log"
	"github.com/miekg/dns"
	"net"
)

var dnsLog log.Logger

func init() {
	dnsLog = log.Get("dns")
}

type Bird struct {
	ip     string
	server *dns.Server
}

func NewDNS(ip string) *Bird {
	return &Bird{
		ip: ip,
	}
}

func (b *Bird) handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)

	// 检查是否有问题查询
	if len(r.Question) == 0 {
		_ = w.WriteMsg(m)
		return
	}

	question := r.Question[0]

	switch question.Qtype {
	case dns.TypeA:
		if question.Name == "im.memobird.cn." {
			rr := &dns.A{
				Hdr: dns.RR_Header{
					Name:   question.Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    60,
				},
				A: net.ParseIP(b.ip),
			}
			m.Answer = append(m.Answer, rr)
		} else {
			m.Rcode = dns.RcodeNameError
		}
	default:
		m.Rcode = dns.RcodeNotImplemented
	}

	_ = w.WriteMsg(m)
}

func (b *Bird) Start() error {
	if b.server != nil {
		panic("server is already running")
	}
	dns.HandleFunc(".", b.handleDNSRequest)
	b.server = &dns.Server{Addr: ":53", Net: "udp"}
	return b.server.ListenAndServe()
}

func (b *Bird) Stop() error {
	if b.server == nil {
		return nil
	}
	err := b.server.Shutdown()
	if err != nil {
		return err
	}
	b.server = nil
	return nil
}
