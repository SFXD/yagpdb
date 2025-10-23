package reputation

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
	"github.com/botlabs-gg/yagpdb/v2/lib/dstate"
	"github.com/botlabs-gg/yagpdb/v2/reputation/models"
)

type reprocessStats struct {
	MessagesProcessed int
	RepGiven          int
	UsersAffected     int
	mu                sync.RWMutex
}

func (s *reprocessStats) Add(messages, rep int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MessagesProcessed += messages
	s.RepGiven += rep
}

func (s *reprocessStats) SetUsers(count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.UsersAffected = count
}

func (s *reprocessStats) Get() (messages, rep, users int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.MessagesProcessed, s.RepGiven, s.UsersAffected
}

// Patterns for detecting reputation
var (
	giveRepPattern = regexp.MustCompile(`(?i)!giverep\s+<@!?(\d+)>`)
	thanksPattern  = regexp.MustCompile(`(?i)thanks\s+<@!?(\d+)>`)
)

// Add helper function for sending error messages
func sendErrorMessage(channelID int64, errorMsg string) {
	msg := fmt.Sprintf("❌ **Error during reprocessing:**\n```\n%s\n```", errorMsg)
	_, err := common.BotSession.ChannelMessageSend(channelID, msg)
	if err != nil {
		logger.WithError(err).Error("Failed to send error message to Discord")
	}
}

func reprocessMessages(ctx context.Context, gs *dstate.GuildSet, conf *models.ReputationConfig, channelID int64) (*reprocessStats, error) {
	stats := &reprocessStats{}
	fiveYearsAgo := time.Now().AddDate(-5, 0, 0)

	// Track reputation changes per user
	repChanges := make(map[int64]int64)

	// Start progress updater goroutine
	stopChan := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sendPeriodicUpdates(channelID, stats, stopChan)
	}()

	// Get all text channels
	channels := gs.Channels

	channelErrors := 0 // Track failed channels

	for _, channel := range channels {
		// 0 = GUILD_TEXT channel type
		if channel.Type != discordgo.ChannelTypeGuildText {
			continue
		}

		// Process channel messages
		processed, changes, err := processChannel(ctx, channel.ID, fiveYearsAgo, conf)
		if err != nil {
			channelErrors++
			errorMsg := fmt.Sprintf("Error processing channel <#%d>: %v", channel.ID, err)
			logger.WithError(err).Errorf("Error processing channel %d", channel.ID)
			sendErrorMessage(channelID, errorMsg) // Send error to Discord
			continue
		}

		// Update stats
		repCount := 0
		for _, amount := range changes {
			repCount += int(amount)
		}
		stats.Add(processed, repCount)

		// Aggregate reputation changes
		for userID, amount := range changes {
			repChanges[userID] += amount
		}

		// Rate limiting between channels
		time.Sleep(1000 * time.Millisecond)
	}

	// Stop progress updater
	close(stopChan)
	wg.Wait()

	// Apply reputation changes to database
	uniqueUsers := 0
	userErrors := 0 // Track failed user updates

	for userID, amount := range repChanges {
		if amount == 0 {
			continue
		}

		uniqueUsers++

		// Use the existing insertUpdateUserRep function
		_, err := insertUpdateUserRep(ctx, gs.ID, userID, amount)
		if err != nil {
			userErrors++
			errorMsg := fmt.Sprintf("Failed to update reputation for user <@%d>: %v", userID, err)
			logger.WithError(err).Errorf("Failed to update rep for user %d", userID)
			sendErrorMessage(channelID, errorMsg) // Send error to Discord
			continue
		}
	}

	stats.SetUsers(uniqueUsers)

	// Send summary if there were errors
	if channelErrors > 0 || userErrors > 0 {
		summaryMsg := fmt.Sprintf("⚠️ **Reprocessing completed with errors:**\n"+
			"- **Channel errors:** %d\n"+
			"- **User update errors:** %d", channelErrors, userErrors)
		sendErrorMessage(channelID, summaryMsg)
	}

	return stats, nil
}

func sendPeriodicUpdates(channelID int64, stats *reprocessStats, stopChan chan struct{}) {
	// Define update intervals: duration and count at each level
	type updateInterval struct {
		duration time.Duration
		count    int
	}

	updateIntervals := []updateInterval{
		{duration: 10 * time.Second, count: 5},  // First 5: every 10 seconds
		{duration: 1 * time.Minute, count: 5},   // Next 5: every minute
		{duration: 1 * time.Hour, count: 5},     // Next 5: every hour
		{duration: 24 * time.Hour, count: -1},   // Unlimited: daily
	}

	startTime := time.Now()
	currentIntervalIdx := 0
	updatesAtCurrentInterval := 0

	ticker := time.NewTicker(updateIntervals[0].duration)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			messages, rep, users := stats.Get()
			elapsed := time.Since(startTime)

			updateMsg := fmt.Sprintf("⏳ **Reprocessing Status Update**\n"+
				"Time elapsed: %s\n"+
				"Messages processed: **%d**\n"+
				"Reputation given: **%d** times\n"+
				"Users affected so far: **%d**\n"+
				"_Still processing..._",
				formatDuration(elapsed), messages, rep, users)

			_, err := common.BotSession.ChannelMessageSend(channelID, updateMsg)
			if err != nil {
				logger.WithError(err).Error("Failed to send progress update")
			}

			// Increment update count for current interval
			updatesAtCurrentInterval++

			// Check if we need to move to next interval
			currentInterval := updateIntervals[currentIntervalIdx]
			if currentInterval.count != -1 && updatesAtCurrentInterval >= currentInterval.count {
				// Move to next interval if available
				if currentIntervalIdx < len(updateIntervals)-1 {
					currentIntervalIdx++
					updatesAtCurrentInterval = 0

					// Update ticker to new duration
					ticker.Stop()
					ticker = time.NewTicker(updateIntervals[currentIntervalIdx].duration)

					logger.Infof("Switching to update interval: %v", updateIntervals[currentIntervalIdx].duration)
				}
			}

		case <-stopChan:
			return
		}
	}
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	} else if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func processChannel(ctx context.Context, channelID int64, since time.Time, conf *models.ReputationConfig) (int, map[int64]int64, error) {
	processed := 0
	repChanges := make(map[int64]int64)

	var beforeID int64 = 0

	for {
		select {
		case <-ctx.Done():
			return processed, repChanges, ctx.Err()
		default:
		}

		// Fetch messages (100 is Discord's limit)
		// Try int64 first, fall back to string if needed
		var messages []*discordgo.Message
		var err error

		if beforeID == 0 {
			messages, err = common.BotSession.ChannelMessages(channelID, 100, 0, 0, 0)
		} else {
			messages, err = common.BotSession.ChannelMessages(channelID, 100, beforeID, 0, 0)
		}

		if err != nil {
			return processed, repChanges, err
		}

		if len(messages) == 0 {
			break
		}

		for _, msg := range messages {
			// Parse message timestamp
			msgTime, err := msg.Timestamp.Parse()
			if err != nil {
				continue
			}

			// Stop if message is older than 5 years
			if msgTime.Before(since) {
				return processed, repChanges, nil
			}

			processed++

			// Skip bot messages
			if msg.Author.Bot {
				continue
			}

			// Check for !giverep pattern
			if matches := giveRepPattern.FindStringSubmatch(msg.Content); matches != nil {
				userID := common.MustParseInt(matches[1])
				if userID != 0 && userID != msg.Author.ID {
					repChanges[userID]++
				}
				continue
			}

			// Check for "thanks @user" pattern (if not disabled)
			if !conf.DisableThanksDetection {
				if matches := thanksPattern.FindStringSubmatch(msg.Content); matches != nil {
					userID := common.MustParseInt(matches[1])
					if userID != 0 && userID != msg.Author.ID {
						repChanges[userID]++
					}
				}
			}
		}

		// Set beforeID to oldest message in batch for next iteration
		beforeID = messages[len(messages)-1].ID

		// Rate limiting - INCREASED to 1 second between message batches
		time.Sleep(1000 * time.Millisecond)
	}

	return processed, repChanges, nil
}

