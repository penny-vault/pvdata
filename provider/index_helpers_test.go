package provider

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIndexHelpers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Index Helpers Suite")
}

var _ = Describe("shouldTakeSnapshot", func() {
	It("returns true when no previous snapshot exists", func() {
		Expect(shouldTakeSnapshot(time.Time{}, "weekly")).To(BeTrue())
	})

	It("returns true when daily and last snapshot was yesterday", func() {
		yesterday := time.Now().AddDate(0, 0, -1)
		Expect(shouldTakeSnapshot(yesterday, "daily")).To(BeTrue())
	})

	It("returns false when daily and last snapshot was today", func() {
		today := time.Now()
		Expect(shouldTakeSnapshot(today, "daily")).To(BeFalse())
	})

	It("returns true when weekly and last snapshot was 8 days ago", func() {
		eightDaysAgo := time.Now().AddDate(0, 0, -8)
		Expect(shouldTakeSnapshot(eightDaysAgo, "weekly")).To(BeTrue())
	})

	It("returns false when weekly and last snapshot was 3 days ago", func() {
		threeDaysAgo := time.Now().AddDate(0, 0, -3)
		Expect(shouldTakeSnapshot(threeDaysAgo, "weekly")).To(BeFalse())
	})

	It("returns true when monthly and last snapshot was 32 days ago", func() {
		thirtyTwoDaysAgo := time.Now().AddDate(0, 0, -32)
		Expect(shouldTakeSnapshot(thirtyTwoDaysAgo, "monthly")).To(BeTrue())
	})

	It("returns false when monthly and last snapshot was 15 days ago", func() {
		fifteenDaysAgo := time.Now().AddDate(0, 0, -15)
		Expect(shouldTakeSnapshot(fifteenDaysAgo, "monthly")).To(BeFalse())
	})

	It("returns true when quarterly and last snapshot was 95 days ago", func() {
		ninetyFiveDaysAgo := time.Now().AddDate(0, 0, -95)
		Expect(shouldTakeSnapshot(ninetyFiveDaysAgo, "quarterly")).To(BeTrue())
	})

	It("defaults to weekly for unknown frequency", func() {
		eightDaysAgo := time.Now().AddDate(0, 0, -8)
		Expect(shouldTakeSnapshot(eightDaysAgo, "bogus")).To(BeTrue())
	})
})

var _ = Describe("diffSnapshots", func() {
	It("returns all as added when previous is empty", func() {
		current := map[string]string{"AAPL": "BBG000B9XRY4", "MSFT": "BBG000BPH459"}
		adds, removes := diffSnapshots(current, map[string]string{})
		Expect(adds).To(HaveLen(2))
		Expect(removes).To(BeEmpty())
	})

	It("returns all as removed when current is empty", func() {
		previous := map[string]string{"AAPL": "BBG000B9XRY4"}
		adds, removes := diffSnapshots(map[string]string{}, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(HaveLen(1))
		Expect(removes).To(HaveKey("AAPL"))
	})

	It("detects additions and removals", func() {
		current := map[string]string{"AAPL": "BBG000B9XRY4", "GOOG": "BBG009S39JX6"}
		previous := map[string]string{"AAPL": "BBG000B9XRY4", "MSFT": "BBG000BPH459"}
		adds, removes := diffSnapshots(current, previous)
		Expect(adds).To(HaveLen(1))
		Expect(adds).To(HaveKey("GOOG"))
		Expect(removes).To(HaveLen(1))
		Expect(removes).To(HaveKey("MSFT"))
	})

	It("returns empty when sets are identical", func() {
		current := map[string]string{"AAPL": "BBG000B9XRY4"}
		previous := map[string]string{"AAPL": "BBG000B9XRY4"}
		adds, removes := diffSnapshots(current, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(BeEmpty())
	})
})
