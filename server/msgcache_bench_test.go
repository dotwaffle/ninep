package server

import (
	"github.com/dotwaffle/ninep/proto"
	"testing"
)

func BenchmarkMetadataAlloc(b *testing.B) {
	// Setup a mock connection with protocolL.
	c := &conn{protocol: protocolL}

	types := []proto.MessageType{
		proto.TypeTremove,
		proto.TypeTmkdir,
		proto.TypeTsetattr,
		proto.TypeTrename,
		proto.TypeTsymlink,
		proto.TypeTmknod,
	}

	for _, t := range types {
		b.Run("type="+t.String(), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				msg, err := c.newMessage(t)
				if err != nil {
					b.Fatal(err)
				}
				// Return to cache.
				putCachedMsg(msg)
			}
		})
	}
}
