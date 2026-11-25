// Package scheduler is the polling loop of `importer serve`: it claims due scheduled sources from the database
// (store.ClaimDue) and runs them, a bounded number at a time. All state lives in the database, so any number of
// replicas can run it; the claim is what guarantees one run per slot.
package scheduler
