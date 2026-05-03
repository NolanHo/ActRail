package app

import "actrail/internal/adapters/iod"

func (h *runtimeIODHelper) iodSummary() *IODRuntimeSummary {
	if h == nil {
		return nil
	}
	return &IODRuntimeSummary{
		BuildDate: h.buildDate,
		GitSHA:    h.gitSHA,
		StartTS:   h.startTS,
		Mode:      "std",
	}
}

func iodSummaryFromHello(hello iod.HelloPacket) *IODRuntimeSummary {
	if hello.IODBuildDate == "" && hello.IODGitSHA == "" && hello.StartTS <= 0 {
		return nil
	}
	return &IODRuntimeSummary{
		BuildDate: hello.IODBuildDate,
		GitSHA:    hello.IODGitSHA,
		StartTS:   hello.StartTS,
		Mode:      "std",
	}
}

func grpcIODSummary() *IODRuntimeSummary {
	return &IODRuntimeSummary{Mode: "grpc"}
}
