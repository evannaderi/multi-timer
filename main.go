package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/beeep"
)

type TimerPhase struct {
	WorkDuration  time.Duration
	BreakDuration time.Duration
}

type TimerState struct {
	isWork       bool
	currentTime  time.Duration
	cycles       int
	currentPhase int
	name         string
	notifText    string
}

type Timer struct {
	state     TimerState
	phases    []TimerPhase
	maxCycles int // -1 for unlimited
	isPaused  bool
}

type TimerConfig struct {
	Name      string
	NotifText string
	Phases    []TimerPhase
	MaxCycles int
}

// MarshalJSON and UnmarshalJSON handle Duration serialization
func (p TimerPhase) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		WorkDuration  int64
		BreakDuration int64
	}{
		WorkDuration:  int64(p.WorkDuration),
		BreakDuration: int64(p.BreakDuration),
	})
}

func (p *TimerPhase) UnmarshalJSON(data []byte) error {
	aux := &struct {
		WorkDuration  int64
		BreakDuration int64
	}{}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	p.WorkDuration = time.Duration(aux.WorkDuration)
	p.BreakDuration = time.Duration(aux.BreakDuration)
	return nil
}

const (
	clearScreen   = "\033[2J"
	moveToTop     = "\033[H"
	clearLine     = "\033[K"
	saveCursor    = "\033[s"
	restoreCursor = "\033[u"
	clearToBottom = "\033[J"
	configFile    = "timers.json"
)

type TimerManager struct {
	activeTimers []*Timer
	configs      []TimerConfig
	displayChan  chan bool
	mu           sync.Mutex
}

func NewTimerManager() *TimerManager {
	return &TimerManager{
		activeTimers: make([]*Timer, 0),
		configs:      make([]TimerConfig, 0),
		displayChan:  make(chan bool, 1),
	}
}

func notify(title, message string) {
	err := beeep.Notify(title, message, "")
	if err != nil {
		fmt.Println("Error sending notification:", err)
	}
}

func (t *Timer) String() string {
	state := "Work"
	if !t.state.isWork {
		state = "Break"
	}

	minutes := int(t.state.currentTime.Minutes())
	seconds := int(t.state.currentTime.Seconds()) % 60

	cycleStr := fmt.Sprintf("%d", t.state.cycles)
	if t.maxCycles == -1 {
		cycleStr += " (∞)"
	} else {
		cycleStr += fmt.Sprintf("/%d", t.maxCycles)
	}

	phaseStr := fmt.Sprintf("Phase %d/%d", t.state.currentPhase+1, len(t.phases))

	return fmt.Sprintf("%s - %s: %02d:%02d (Cycle %s) %s",
		t.state.name, state, minutes, seconds, cycleStr, phaseStr)
}

// Update handles the timer state update
func (t *Timer) update() bool {
	if t.isPaused {
		return false
	}

	if t.state.currentTime <= 0 {
		currentPhase := t.phases[t.state.currentPhase]
		if t.state.isWork {
			notify(t.state.name, fmt.Sprintf("Break: %s", t.state.notifText))
			t.state.isWork = false
			t.state.currentTime = currentPhase.BreakDuration
		} else {
			t.state.cycles++
			if t.maxCycles != -1 && t.state.cycles > t.maxCycles {
				t.state.currentPhase++
				if t.state.currentPhase >= len(t.phases) {
					notify(t.state.name, fmt.Sprintf("All phases completed: %s", t.state.notifText))
					return true // Timer completed
				}
				t.state.cycles = 1
			}
			notify(t.state.name, t.state.notifText)
			t.state.isWork = true
			t.state.currentTime = t.phases[t.state.currentPhase].WorkDuration
		}
		return false
	}
	t.state.currentTime -= time.Second
	return false
}

func (tm *TimerManager) startUpdateLoop() {
	ticker := time.NewTicker(time.Second)
	go func() {
		for range ticker.C {
			tm.mu.Lock()
			needsDisplay := false

			for i := len(tm.activeTimers) - 1; i >= 0; i-- {
				timer := tm.activeTimers[i]
				completed := timer.update()
				if completed {
					// Remove completed timer
					tm.activeTimers = append(tm.activeTimers[:i], tm.activeTimers[i+1:]...)
				}
				needsDisplay = true
			}

			tm.mu.Unlock()

			if needsDisplay {
				// Non-blocking send to display channel
				select {
				case tm.displayChan <- true:
				default:
				}
			}
		}
	}()
}

func saveTimerConfigs(configs []TimerConfig) error {
	file, err := os.Create(configFile)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(configs)
}

func loadTimerConfigs() ([]TimerConfig, error) {
	file, err := os.Open(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []TimerConfig{}, nil
		}
		return nil, err
	}
	defer file.Close()

	var configs []TimerConfig
	err = json.NewDecoder(file).Decode(&configs)
	return configs, err
}

func parseDuration(input string) (time.Duration, error) {
	input = strings.TrimSpace(input)
	if strings.Contains(input, ":") {
		parts := strings.Split(input, ":")
		if len(parts) != 2 {
			return 0, fmt.Errorf("invalid format, use MM:SS")
		}
		minutes, err1 := strconv.Atoi(parts[0])
		seconds, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return 0, fmt.Errorf("invalid numbers")
		}
		return time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second, nil
	}
	minutes, err := strconv.Atoi(input)
	if err != nil {
		return 0, err
	}
	return time.Duration(minutes) * time.Minute, nil
}

func clearDisplay() {
	fmt.Print(clearScreen, moveToTop)
}

func (tm *TimerManager) displayTimers(preserveCommandLine bool) {
	if preserveCommandLine {
		fmt.Print(saveCursor, moveToTop)
	} else {
		clearDisplay()
	}

	fmt.Print(clearLine, "=== Active Timers ===\n")

	tm.mu.Lock()
	for i, timer := range tm.activeTimers {
		fmt.Print(clearLine)
		status := ""
		if timer.isPaused {
			status = " (PAUSED)"
		}
		fmt.Printf("%d. %s%s\n", i+1, timer.String(), status)
	}
	tm.mu.Unlock()

	fmt.Print(clearLine, "\nCommands:\n")
	fmt.Print(clearLine, "a - Add new timer\n")
	fmt.Print(clearLine, "p <number> - Pause/Resume timer\n")
	fmt.Print(clearLine, "r <number> - Reset timer\n")
	fmt.Print(clearLine, "d <number> - Delete timer\n")
	fmt.Print(clearLine, "q - Quit\n")

	if preserveCommandLine {
		fmt.Print(restoreCursor)
	}
}

func readLine(reader *bufio.Reader, prompt string) string {
	fmt.Print(prompt)
	text, _ := reader.ReadString('\n')
	return strings.TrimSpace(text)
}

func createTimer() (*Timer, *TimerConfig) {
	reader := bufio.NewReader(os.Stdin)

	name := readLine(reader, "Enter timer name: ")
	notifText := readLine(reader, "Enter notification text: ")

	var phases []TimerPhase
	for {
		fmt.Println("\nPhase", len(phases)+1)
		fmt.Println("Enter work time (MM:SS or just minutes): ")
		workStr := readLine(reader, "")
		workDur, err := parseDuration(workStr)
		if err != nil {
			fmt.Println("Invalid duration format. Try again.")
			continue
		}

		fmt.Println("Enter break time (MM:SS or just minutes): ")
		breakStr := readLine(reader, "")
		breakDur, err := parseDuration(breakStr)
		if err != nil {
			fmt.Println("Invalid duration format. Try again.")
			continue
		}

		phases = append(phases, TimerPhase{workDur, breakDur})

		fmt.Print("Add another phase? (y/n): ")
		if strings.ToLower(readLine(reader, "")) != "y" {
			break
		}
	}

	cycleType := readLine(reader, "Enter cycle type (u for unlimited, number for fixed cycles): ")
	maxCycles := -1
	if cycleType != "u" {
		cycles, err := strconv.Atoi(cycleType)
		if err == nil && cycles > 0 {
			maxCycles = cycles
		}
	}

	config := &TimerConfig{
		Name:      name,
		NotifText: notifText,
		Phases:    phases,
		MaxCycles: maxCycles,
	}

	timer := &Timer{
		state: TimerState{
			isWork:       true,
			currentTime:  phases[0].WorkDuration,
			cycles:       1,
			currentPhase: 0,
			name:         name,
			notifText:    notifText,
		},
		phases:    phases,
		maxCycles: maxCycles,
		isPaused:  false,
	}

	return timer, config
}

func timerFromConfig(config TimerConfig) *Timer {
	return &Timer{
		state: TimerState{
			isWork:       true,
			currentTime:  config.Phases[0].WorkDuration,
			cycles:       1,
			currentPhase: 0,
			name:         config.Name,
			notifText:    config.NotifText,
		},
		phases:    config.Phases,
		maxCycles: config.MaxCycles,
		isPaused:  false,
	}
}

func main() {
	tm := NewTimerManager()

	// Load saved timer configurations
	configs, err := loadTimerConfigs()
	if err != nil {
		fmt.Println("Error loading timer configurations:", err)
	} else {
		tm.configs = configs
	}

	// Load all timers
	for _, config := range tm.configs {
		timer := timerFromConfig(config)
		tm.mu.Lock()
		tm.activeTimers = append(tm.activeTimers, timer)
		tm.mu.Unlock()
	}

	// Start the central update loop
	tm.startUpdateLoop()

	// Start display update goroutine
	go func() {
		for range tm.displayChan {
			tm.displayTimers(true)
		}
	}()

	clearDisplay()
	tm.displayTimers(false)
	fmt.Print("\nEnter command: ")

	reader := bufio.NewReader(os.Stdin)

	for {
		command, _ := reader.ReadString('\n')
		command = strings.TrimSpace(command)

		if len(command) == 0 {
			fmt.Print("Enter command: ")
			continue
		}

		switch strings.ToLower(command[0:1]) {
		case "a":
			timer, config := createTimer()
			tm.mu.Lock()
			tm.activeTimers = append(tm.activeTimers, timer)
			tm.configs = append(tm.configs, *config)
			tm.mu.Unlock()
			if err := saveTimerConfigs(tm.configs); err != nil {
				fmt.Println("Error saving timer configurations:", err)
			}
			tm.displayTimers(false)
			fmt.Print("\nEnter command: ")

		case "p":
			var num int
			fmt.Sscanf(command, "p %d", &num)
			if num > 0 && num <= len(tm.activeTimers) {
				tm.mu.Lock()
				tm.activeTimers[num-1].isPaused = !tm.activeTimers[num-1].isPaused
				tm.mu.Unlock()
				tm.displayTimers(false)
			}
			fmt.Print("\nEnter command: ")

		case "r":
			var num int
			fmt.Sscanf(command, "r %d", &num)
			if num > 0 && num <= len(tm.activeTimers) {
				tm.mu.Lock()
				tm.activeTimers[num-1].state.currentTime = tm.activeTimers[num-1].phases[0].WorkDuration
				tm.mu.Unlock()
				tm.displayTimers(false)
			}
			fmt.Print("\nEnter command: ")

		case "d":
			var num int
			fmt.Sscanf(command, "d %d", &num)
			if num > 0 && num <= len(tm.activeTimers) {
				tm.mu.Lock()
				tm.activeTimers = append(tm.activeTimers[:num-1], tm.activeTimers[num:]...)
				tm.configs = append(tm.configs[:num-1], tm.configs[num:]...)
				tm.mu.Unlock()
				if err := saveTimerConfigs(tm.configs); err != nil {
					fmt.Println("Error saving timer configurations:", err)
				}
				tm.displayTimers(false)
			}
			fmt.Print("\nEnter command: ")

		case "q":
			return

		default:
			fmt.Print("Enter command: ")
		}
	}
}
