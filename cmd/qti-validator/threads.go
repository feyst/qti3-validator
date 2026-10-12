package main

import "log/slog"

// addThread gives the Go runtime one thread more than there are CPUs, and
// returns the number of CPUs. set is runtime.GOMAXPROCS, and gomaxprocs the
// value of GOMAXPROCS in the environment.
//
// The service runs MAX_CONCURRENT validations, by default one per CPU, and
// one small one beside them on the express slot (httpapi). With as many
// threads as CPUs, a small validation that arrives while the others keep
// every CPU busy waits until the runtime takes a thread from one of them,
// which it does after 10 ms or more. With one thread more it starts at
// once, and the kernel shares the CPUs between the threads: measured with
// 2 CPUs beside 8 clients sending 1 MiB documents, a typical item then
// takes 2 ms (median; 7 ms for 9 in 10) instead of 13 ms (32 ms), and the
// large documents are 4% slower.
//
// The number of CPUs is the GOMAXPROCS the runtime chose, which follows the
// CPU limit of the container. Everything that is sized to the CPUs takes
// the number returned here, not the number of threads: the default of
// MAX_CONCURRENT and what follows from it, and whether the schemas compile
// at the same time. A GOMAXPROCS set in the environment is kept as it is,
// and is then the number of CPUs as well. Setting the number here also
// fixes it: the runtime no longer follows a CPU limit that changes while
// the service runs.
func addThread(log *slog.Logger, gomaxprocs string, set func(int) int) (cpus int) {
	cpus = set(0)
	if gomaxprocs != "" {
		log.Info("GOMAXPROCS is set and kept", "gomaxprocs", gomaxprocs, "cpus", cpus)
		return cpus
	}
	set(cpus + 1)
	log.Info("one thread more than CPUs, for the express slot", "cpus", cpus, "gomaxprocs", cpus+1)
	return cpus
}
