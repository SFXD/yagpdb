package stdcommands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/botlabs-gg/yagpdb/v2/commands"
	"github.com/botlabs-gg/yagpdb/v2/lib/dcmd"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
)

// SalesforceStatus represents the JSON response structure
type SalesforceStatus struct {
	Key            string `json:"key"`
	Location       string `json:"location"`
	Environment    string `json:"environment"`
	ReleaseVersion string `json:"releaseVersion"`
	Status         string `json:"status"`
	IsActive       bool   `json:"isActive"`
	GeneralMessages []struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	} `json:"GeneralMessages"`
}

var Command_sfstatus = &commands.YAGCommand{
	CmdCategory:  commands.CategoryTool,
	Name:         "sfstatus",
	Description:  "Checks Salesforce instance status",
	RequiredArgs: 1,
	Arguments: []*dcmd.ArgDef{
		{Name: "Instance", Type: dcmd.String},
	},
	RunFunc: func(data *dcmd.Data) (interface{}, error) {
		instance := strings.ToUpper(data.Args[0].Str())

		// Fetch status from Salesforce API
		url := fmt.Sprintf("https://status.salesforce.com/api/instances/%s/status", instance)
		resp, err := http.Get(url)
		if err != nil {
			return "Error: Unable to fetch status from Salesforce API", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return fmt.Sprintf("Error: Instance '%s' not found or API error (HTTP %d)", instance, resp.StatusCode), nil
		}

		// Read response body
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "Error: Unable to read API response", err
		}

		// Parse JSON response
		var status SalesforceStatus
		if err := json.Unmarshal(body, &status); err != nil {
			return "Error: Unable to parse JSON response", err
		}

		// Determine status color and emoji
		statusColor := 0x2ECC71 // Green for OK
		statusEmoji := "✅"

		switch status.Status {
		case "OK":
			statusColor = 0x2ECC71 // Green
			statusEmoji = "✅"
		case "MAJOR_INCIDENT_CORE", "MAINTENANCE":
			statusColor = 0xE74C3C // Red
			statusEmoji = "🔴"
		case "MINOR_INCIDENT_CORE", "PERFORMANCE_DEGRADATION":
			statusColor = 0xF39C12 // Yellow/Orange
			statusEmoji = "⚠️"
		default:
			statusColor = 0x95A5A6 // Gray
			statusEmoji = "❓"
		}

		// Build embed
		embed := &discordgo.MessageEmbed{
			Title: fmt.Sprintf("%s Salesforce Instance: %s", statusEmoji, status.Key),
			Color: statusColor,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "Status",
					Value:  status.Status,
					Inline: true,
				},
				{
					Name:   "Location",
					Value:  status.Location,
					Inline: true,
				},
				{
					Name:   "Environment",
					Value:  status.Environment,
					Inline: true,
				},
				{
					Name:   "Release Version",
					Value:  status.ReleaseVersion,
					Inline: false,
				},
			},
			Footer: &discordgo.MessageEmbedFooter{
				Text: fmt.Sprintf("Active: %t", status.IsActive),
			},
		}

		// Add general messages if any exist
		if len(status.GeneralMessages) > 0 {
			messagesText := ""
			for i, msg := range status.GeneralMessages {
				if i >= 3 { // Limit to 3 messages to avoid embed size limits
					messagesText += fmt.Sprintf("\n...and %d more messages", len(status.GeneralMessages)-3)
					break
				}
				messagesText += fmt.Sprintf("**%s**\n%s\n\n", msg.Subject, truncateString(msg.Body, 200))
			}

			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Recent Messages",
				Value:  messagesText,
				Inline: false,
			})
		}

		return embed, nil
	},
}

// Helper function to truncate long strings
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

