/*
	Sent when the owner of a table clicks on the "Start Game" button
	(the client will send a "hello" message after getting "gameStart")

	"data" is empty
*/

package main

import (
	"encoding/json"
	"hash/crc64"
	"io/ioutil"
	"math/rand"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func commandGameStart(s *Session, d *CommandData) {
	/*
		Validation
	*/

	// Validate that the game exists
	gameID := s.CurrentGame()
	var g *Game
	if v, ok := games[gameID]; !ok {
		s.Warning("Game " + strconv.Itoa(gameID) + " does not exist.")
		return
	} else {
		g = v
	}

	// Validate that the game has at least 2 players
	if len(g.Players) < 2 {
		s.Warning("You need at least 2 players before you can start a game.")
		return
	}

	// Validate that the game is not started yet
	if g.Running {
		s.Warning("That game has already started, so you cannot start it.")
		return
	}

	// Validate that this is the owner of the game
	if g.Owner != s.UserID() {
		s.Warning("Only the owner of a game can start it.")
		return
	}

	/*
		Start
	*/

	log.Info(g.GetName() + "Starting the game.")
	g.Running = true
	g.DatetimeStarted = time.Now()

	// Start the idle timeout
	go g.CheckIdle()

	// Create the deck
	// (it will have 60 cards if playing no variant,
	// 65 cards if playing a one of each variant,
	// and 70 cards when playing the other variants)
	suits := make([]int, 0)
	for i := 0; i < len(variants[g.Options.Variant].Suits); i++ {
		suits = append(suits, i) // For a normal game, the suits will be equal to {0, 1, 2, 3, 4}
	}
	for _, suit := range suits {
		ranks := []int{1, 2, 3, 4, 5}
		if strings.HasPrefix(g.Options.Variant, "Up or Down") {
			ranks = append(ranks, startCardRank) // This is defined in the "variantUpOrDown.go" file
		}
		for _, rank := range ranks {
			// In a normal suit of Hanabi, there are three 1's, two 2's, two 3's, two 4's, and one five
			var amountToAdd int
			if rank == 1 {
				amountToAdd = 3
				if strings.HasPrefix(g.Options.Variant, "Up or Down") {
					amountToAdd = 1
				}
			} else if rank == 5 {
				amountToAdd = 1
			} else if rank == startCardRank { // The "START" card
				amountToAdd = 1
			} else {
				amountToAdd = 2
			}
			if variants[g.Options.Variant].Suits[suit].IsOneOfEach {
				amountToAdd = 1
			}

			for i := 0; i < amountToAdd; i++ {
				// Add the card to the deck
				card := &Card{
					Suit: suit,
					Rank: rank,
					// We can't set the order here because the deck will be shuffled later
				}
				g.Deck = append(g.Deck, card)
			}
		}
	}

	// Create the stacks
	for i := 0; i < len(suits); i++ {
		g.Stacks = append(g.Stacks, 0)
		g.StackDirections = append(g.StackDirections, 0)
	}
	g.MaxScore = len(g.Stacks) * 5 // Assuming that there are 5 points per stack

	// Handle setting the seed
	preset := false
	seedPrefix := "p" + strconv.Itoa(len(g.Players)) + "v" + strconv.Itoa(variants[g.Options.Variant].ID) + "s"
	if g.Options.SetSeed != "" {
		// This is a game with a preset seed
		g.Seed = seedPrefix + g.Options.SetSeed
	} else if g.Options.SetReplay != 0 {
		// This is a replay of an existing game
		if v, err := db.Games.GetSeed(g.Options.SetReplay); err != nil {
			log.Error("Failed to get the seed for game \""+strconv.Itoa(g.Options.SetReplay)+"\":", err)
			s.Error("Failed to create the game. Please contact an administrator.")
			return
		} else {
			g.Seed = v
		}
	} else if g.Options.SetDeal != "" {
		// This is a preset deal from a file, so just set the seed equal to the file name
		g.Seed = g.Options.SetDeal
		preset = true // Later on, we need to skip shuffling the deck
		if !g.SetPresetDeck(s) {
			return
		}
	} else {
		// This is a normal game with a random seed / a random deck
		// Get a list of all the seeds that these players have played before
		seedMap := make(map[string]bool)
		for _, p := range g.Players {
			var seeds []string
			if v, err := db.Games.GetPlayerSeeds(p.ID); err != nil {
				log.Error("Failed to get the past seeds for \""+s.Username()+"\":", err)
				s.Error("Failed to create the game. Please contact an administrator.")
				return
			} else {
				seeds = v
			}

			for _, v := range seeds {
				seedMap[v] = true
			}
		}

		// Find a seed that no-one has played before
		seedNum := 0
		looking := true
		for looking {
			seedNum++
			g.Seed = seedPrefix + strconv.Itoa(seedNum)
			if !seedMap[g.Seed] {
				looking = false
			}
		}
	}
	log.Info(g.GetName()+"Using seed:", g.Seed)

	// Seed the random number generator with the game seed
	// We use the CRC64 hash function to convert the string to an uint64
	// (seeding with negative numbers will not work)
	// https://www.socketloop.com/references/golang-hash-crc64-checksum-and-maketable-functions-example
	crc64Table := crc64.MakeTable(crc64.ECMA)
	intSeed := crc64.Checksum([]byte(g.Seed), crc64Table)
	rand.Seed(int64(intSeed))

	// Shuffle the deck
	// From: https://stackoverflow.com/questions/12264789/shuffle-array-in-go
	if !preset {
		for i := range g.Deck {
			j := rand.Intn(i + 1)
			g.Deck[i], g.Deck[j] = g.Deck[j], g.Deck[i]
		}
	}

	// Mark the order of all of the cards in the deck
	for i, c := range g.Deck {
		c.Order = i
	}

	// Log the deal (so that it can be distributed to others if necessary)
	log.Info("--------------------------------------------------")
	log.Info("Deal for seed: " + g.Seed + " (from top to bottom)")
	log.Info("(cards are dealt to a player until their hand fills up before moving on to the next one)")
	for i, c := range g.Deck {
		log.Info(strconv.Itoa(i+1) + ") " + c.Name(g))
	}
	log.Info("--------------------------------------------------")

	// Initialize all of the player's notes based on the number of cards in the deck
	for _, p := range g.Players {
		p.Notes = make([]string, len(g.Deck))
	}

	// Get a random player to start first (based on the game seed)
	// (but skip doing this if we are playing a preset deal from a file,
	// since the player that goes first is specified on the file line of the file)
	if !preset {
		g.ActivePlayer = rand.Intn(len(g.Players))
	}

	// Decide the random character assignments
	// (this has to be after seed generation and initialization)
	characterGenerate(g)

	// Initialize all of the players to not being present
	// This is so that we don't send them unnecessary messages during the game initialization
	// (we will set them back to present once they send the "ready" message)
	listOfAwayPlayers := make([]int, 0)
	for _, p := range g.Players {
		if p.Present {
			p.Present = false
		} else {
			listOfAwayPlayers = append(listOfAwayPlayers, p.ID)
		}
	}

	if g.Options.SetReplay != 0 {
		// We are replaying an existing game up to a certain point,
		// so we don't need to deal cards or add the initial actions
		g.CopyActionsFromDatabase(s)
	} else {
		// This is a normal game
		// Deal the cards
		handSize := 5
		if len(g.Players) > 3 {
			handSize = 4
		}
		for _, p := range g.Players {
			for i := 0; i < handSize; i++ {
				p.DrawCard(g)
			}
		}

		// Record the initial status of the game
		g.NotifyStatus(false) // The argument is "doubleDiscard"

		// Show who goes first
		// (this must be sent before the "turn" message so that the text appears on the first turn of the replay)
		text := g.Players[g.ActivePlayer].Name + " goes first"
		g.Actions = append(g.Actions, ActionText{
			Type: "text",
			Text: text,
		})
		log.Info(g.GetName() + text)

		// Record a message about the first turn
		g.NotifyTurn()
	}

	// Send a "gameStart" message to everyone in the game
	for _, p := range g.Players {
		// If a player is back in the lobby, then don't automatically force them into the game
		if !intInSlice(p.ID, listOfAwayPlayers) {
			p.Session.NotifyGameStart()
		}
	}

	// Let everyone know that the game has started, which will turn the
	// "Join Game" button into "Spectate"
	notifyAllTable(g)

	// Set the status for all of the users in the game
	for _, p := range g.Players {
		p.Session.Set("status", statusPlaying)
		notifyAllUser(p.Session)
	}

	// Start the timer
	g.TurnBeginTime = time.Now()
	if g.Options.Timed {
		go g.CheckTimer(0, g.Players[g.ActivePlayer])
	}
}

func (g *Game) SetPresetDeck(s *Session) bool {
	filePath := path.Join(projectPath, "specific-deals", g.Seed+".txt")
	log.Info("Using a preset deal of:", filePath)

	var lines []string
	if v, err := ioutil.ReadFile(filePath); err != nil {
		log.Error("Failed to read \""+filePath+"\":", err)
		s.Error("Failed to create the game. Please contact an administrator.")
		return false
	} else {
		lines = strings.Split(string(v), "\n")
	}

	var cardRegExp *regexp.Regexp
	if v, err := regexp.Compile(`^(\w)(\d)$`); err != nil {
		log.Error("Failed to create the card regular expression:", err)
		s.Error("Failed to create the game. Please contact an administrator.")
		return false
	} else {
		cardRegExp = v
	}

	for i, line := range lines {
		// The first line is a number that signifies which player will go first
		if i == 0 {
			if v, err := strconv.Atoi(line); err != nil {
				log.Error("Failed to parse the first line (that signifies which player will go first):", line)
				s.Error("Failed to create the game. Please contact an administrator.")
				return false
			} else {
				// Player 1 would be equal to the player at index 0
				g.ActivePlayer = v - 1
			}
			continue
		}

		// Ignore empty lines (the last line of the file might be empty)
		if line == "" {
			continue
		}

		// Parse the line for the suit and the rank
		match2 := cardRegExp.FindStringSubmatch(line)
		if match2 == nil {
			log.Error("Failed to parse line "+strconv.Itoa(i+1)+":", line)
			s.Error("Failed to start the game. Please contact an administrator.")
			return false
		}

		// Change the suit of all of the cards in the deck
		suit := match2[1]
		newSuit := -1
		if suit == "b" {
			newSuit = 0
		} else if suit == "g" {
			newSuit = 1
		} else if suit == "y" {
			newSuit = 2
		} else if suit == "r" {
			newSuit = 3
		} else if suit == "p" {
			newSuit = 4
		} else if suit == "m" {
			newSuit = 5
		} else {
			log.Error("Failed to parse the suit on line "+strconv.Itoa(i+1)+":", suit)
			s.Error("Failed to create the game. Please contact an administrator.")
			return false
		}
		g.Deck[i-1].Suit = newSuit // The first line is the number of players, so we have to subtract one

		// Change the rank of all of the cards in the deck
		rank := match2[2]
		newRank := -1
		if v, err := strconv.Atoi(rank); err != nil {
			log.Error("Failed to parse the rank on line "+strconv.Itoa(i+1)+":", rank)
			s.Error("Failed to create the game. Please contact an administrator.")
			return false
		} else {
			newRank = v
		}
		g.Deck[i-1].Rank = newRank // The first line is the number of players, so we have to subtract one
	}

	return true
}

func (g *Game) CopyActionsFromDatabase(s *Session) bool {
	var actionStrings []string
	if v, err := db.GameActions.GetAll(g.Options.SetReplay); err != nil {
		log.Error("Failed to get the actions from the database for game "+
			strconv.Itoa(g.Options.SetReplay)+":", err)
		s.Error("Failed to initialize the game. Please contact an administrator.")
		return false
	} else {
		actionStrings = v
	}

	for _, actionString := range actionStrings {
		// Convert it from JSON
		var action interface{}
		if err := json.Unmarshal([]byte(actionString), &action); err != nil {
			log.Error("Failed to unmarshal an action:", err)
			s.Error("Failed to initialize the game. Please contact an administrator.")
			return false
		}

		g.Actions = append(g.Actions, action)

		// Stop if we have reached the intended turn
		log.Debug("APPENDED:", len(g.Actions), action)
		if actionTurn, ok := action.(*ActionTurn); ok {
			log.Debug("GETTING HERE:", actionTurn)
			if actionTurn.Num == g.Options.SetReplayTurn {
				return true
			}
		}
	}

	log.Error("Failed to find the intended turn before reaching the end of game " +
		strconv.Itoa(g.Options.SetReplay) + ".")
	s.Error("Failed to initialize the game. Please contact an administrator.")
	return false
}
