// Package eval measures whether wenna is good enough for a founder running a
// business on it. It holds tests only:
//
//   - recall_test.go: how often recall finds the right memory for the
//     queries models send. Deterministic; runs in make ci and fails below
//     a recorded floor.
//   - founder_test.go (build tag eval): real AI apps, in fresh sessions,
//     work through a founder's month on one shared memory. Calls model
//     providers with synthetic data and costs money: make eval.
package eval
