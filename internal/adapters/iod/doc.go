// Package iod freezes the minimal actrail-iod transport contract shared by the
// future helper implementation and the server-side client.
//
// Packet 1 owns only names, envelopes, WAL headers, replay cursors, and
// projection boundaries. Class-specific payload schemas stay opaque here.
package iod
