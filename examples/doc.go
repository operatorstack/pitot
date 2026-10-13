// Package examples includes two small JSON-Lines reference programs:
//
//	token-meter    a passive Consumer for application-supplied model.usage events
//	local-approval a Controller that returns one correlated response per request
//
// The shipped runtime delivers action.requested events, not model token usage.
// Connecting token-meter to it therefore does not collect token counts. Each
// example is a standalone main package and reads one JSON record per line from
// standard input. Controller responses must use the wire framing and match the
// pending action and registered Controller; schema validity alone is insufficient.
// The supervisory-control directory contains a larger offline delivery example.
package examples
