package traceprocessing

import "github.com/baum-ai/baum/sessions/session"

type IDNamespace []string

func NewIDNamespace(parts ...string) IDNamespace {
	return append(IDNamespace(nil), parts...)
}

func (n IDNamespace) Mint(kind string, parts ...string) string {
	return session.MintID(kind, append(append([]string(nil), n...), parts...)...)
}

func AppendVendorID(rows []session.VendorID, vendor, sessionID, ourID string, ourType session.EntityType, kind, vendorID string, sourceRef *string) []session.VendorID {
	if vendorID == "" {
		return rows
	}
	return append(rows, session.VendorID{
		OurID: ourID, OurType: ourType, SessionID: sessionID, Vendor: vendor,
		VendorIDKind: kind, VendorID: vendorID, SourceRef: sourceRef,
	})
}
