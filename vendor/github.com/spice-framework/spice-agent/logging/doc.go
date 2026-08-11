// Package logging projects Spice Agent events into the Spice-native structured
// logger through one bounded best-effort mailbox. It never treats diagnostic
// logging as durable history or execution authority.
package logging

// ContractVersion identifies the safe Agent event projection.
const ContractVersion = "spice.agent.logging/v1"
