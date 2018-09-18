package console

import (
	"fmt"
	"sync"

	"time"

	"sort"

	"image/color"

	"github.com/BigJk/ramen"
	"github.com/BigJk/ramen/consolecolor"
	"github.com/BigJk/ramen/font"
	"github.com/BigJk/ramen/t"
	"github.com/hajimehoshi/ebiten"
	"github.com/hajimehoshi/ebiten/ebitenutil"
)

var emptyCell = ramen.Cell{
	Foreground: consolecolor.New(255, 255, 255),
}

// Console represents a emulated console view
type Console struct {
	Title       string
	Width       int
	Height      int
	Font        *font.Font
	ShowFPS     bool
	SubConsoles []*Console

	parent       *Console
	x            int
	y            int
	priority     int
	isSubConsole bool

	mtx       *sync.RWMutex
	updates   []int
	buffer    [][]ramen.Cell
	lastFrame int64

	lines []*ebiten.Image

	preRenderHook  func(screen *ebiten.Image, timeElapsed float64) error
	postRenderHook func(screen *ebiten.Image, timeElapsed float64) error
}

// New creates a new console
func New(width, height int, font *font.Font, title string) (*Console, error) {
	buf := make([][]ramen.Cell, width)
	for x := range buf {
		buf[x] = make([]ramen.Cell, height)
		for y := range buf[x] {
			buf[x][y] = emptyCell
		}
	}

	lines := make([]*ebiten.Image, width)
	for i := range lines {
		line, err := ebiten.NewImage(font.TileWidth, height*font.TileHeight, ebiten.FilterNearest)
		if err != nil {
			return nil, err
		}
		lines[i] = line
	}

	return &Console{
		Title:       title,
		Width:       width,
		Height:      height,
		Font:        font,
		SubConsoles: make([]*Console, 0),
		mtx:         new(sync.RWMutex),
		updates:     make([]int, 0),
		buffer:      buf,
		lines:       lines,
	}, nil
}

// SetPreRenderHook will apply a hook that gets triggered before the console started rendering
func (c *Console) SetPreRenderHook(hook func(screen *ebiten.Image, timeElapsed float64) error) error {
	if c.isSubConsole {
		return fmt.Errorf("can't hook into sub-console")
	}
	c.preRenderHook = hook
	return nil
}

// SetPostRenderHook will apply a hook that gets triggered after the console is finished rendering
func (c *Console) SetPostRenderHook(hook func(screen *ebiten.Image, timeElapsed float64) error) error {
	if c.isSubConsole {
		return fmt.Errorf("can't hook into sub-console")
	}
	c.preRenderHook = hook
	return nil
}

// SetPriority sets the priority of the console. A higher priority will result in the console
// being drawn on top of all the ones with lower priority.
func (c *Console) SetPriority(priority int) error {
	if !c.isSubConsole {
		return fmt.Errorf("priority of the main console can't be changed")
	}
	c.priority = priority
	c.parent.sortSubConsoles()
	return nil
}

// CreateSubConsole creates a new sub-console
func (c *Console) CreateSubConsole(x, y, width, height int) (*Console, error) {
	if x < 0 || y < 0 || x+width > c.Width || y+height > c.Height || width <= 0 || height <= 0 {
		return nil, fmt.Errorf("sub-console is out of bounds")
	}

	c.mtx.Lock()

	sub, err := New(width, height, c.Font, "")
	if err != nil {
		return nil, err
	}

	sub.parent = c
	sub.x = x
	sub.y = y
	sub.isSubConsole = true

	c.SubConsoles = append(c.SubConsoles, sub)

	c.mtx.Unlock()

	c.sortSubConsoles()

	return sub, nil
}

// RemoveSubConsole removes a sub-console from his parent
func (c *Console) RemoveSubConsole(con *Console) error {
	c.mtx.Lock()
	for i := range c.SubConsoles {
		if c.SubConsoles[i] == con {
			c.SubConsoles[i] = c.SubConsoles[len(c.SubConsoles)-1]
			c.SubConsoles[len(c.SubConsoles)-1] = nil
			c.SubConsoles = c.SubConsoles[:len(c.SubConsoles)-1]
			c.mtx.Unlock()

			c.sortSubConsoles()

			return nil
		}
	}
	c.mtx.Unlock()
	return fmt.Errorf("sub-console not found")
}

// Start will open the console window with the given scale
func (c *Console) Start(scale float64) error {
	if c.isSubConsole {
		return fmt.Errorf("only the main console can be started")
	}
	return ebiten.Run(c.update, c.Width*c.Font.TileWidth, c.Height*c.Font.TileHeight, scale, c.Title)
}

// ClearAll clears the whole console. If no transformer are specified the console will be cleared
// to the default cell look.
func (c *Console) ClearAll(transformer ...t.Transformer) {
	c.Clear(0, 0, c.Width, c.Height, transformer...)
}

// Clear clears part of the console. If no transformer are specified the console will be cleared
// to the default cell look.
func (c *Console) Clear(x, y, width, height int, transformer ...t.Transformer) error {
	c.mtx.Lock()

	for px := 0; px < width; px++ {
		mustUpdate := false
		for py := 0; py < height; py++ {
			if len(transformer) == 0 {
				if c.buffer[px+x][py+y] != emptyCell {
					c.buffer[px+x][py+y] = emptyCell
					mustUpdate = true
				} else {
					for i := range transformer {
						changed, err := transformer[i].Transform(&c.buffer[x][y])
						if err != nil {
							return err
						}
						if changed {
							mustUpdate = true
						}
					}
				}
			}
		}

		if mustUpdate {
			c.updates = append(c.updates, px+x)
		}
	}

	c.mtx.Unlock()

	return nil
}

// Transform transforms a cell. This can be used to change the character, foreground and
// background of a cell or apply custom transformers onto a cell.
func (c *Console) Transform(x, y int, transformer ...t.Transformer) error {
	if len(transformer) == 0 {
		return fmt.Errorf("no transformer given")
	}

	c.mtx.Lock()

	mustUpdate := false
	for i := range transformer {
		changed, err := transformer[i].Transform(&c.buffer[x][y])
		if err != nil {
			return err
		}
		if changed {
			mustUpdate = true
		}
	}

	if mustUpdate {
		c.queueUpdate(x)
	}

	c.mtx.Unlock()

	return nil
}

// Print prints a text onto the console. To give the text a different foreground or
// background color use transformer.
func (c *Console) Print(x, y int, text string, transformer ...t.Transformer) {
	if y >= c.Height {
		return
	}

	for i := range text {
		if x+i >= c.Width {
			return
		}
		c.Transform(x+i, y, append(transformer, t.CharByte(text[i]))...)
	}
}

func (c *Console) sortSubConsoles() {
	c.mtx.Lock()
	sort.Slice(c.SubConsoles, func(i, j int) bool {
		return c.SubConsoles[i].priority > c.SubConsoles[j].priority
	})
	c.mtx.Unlock()
}

func (c *Console) queueUpdate(x int) {
	for i := range c.updates {
		if c.updates[i] == x {
			return
		}
	}
	c.updates = append(c.updates, x)
}

func (c *Console) checkOutOfBounds(x, y int) error {
	if x < 0 || y < 0 || x >= c.Width || y >= c.Height {
		return fmt.Errorf("position out of bounds")
	}
	return nil
}

func (c *Console) updateLine(x int) {
	c.lines[x].Fill(color.NRGBA{0, 0, 0, 0})
	for y := range c.buffer[x] {
		if c.buffer[x][y].Background.A > 0 {
			ebitenutil.DrawRect(c.lines[x], 0, float64(y*c.Font.TileHeight), float64(c.Font.TileWidth), float64(c.Font.TileHeight), c.buffer[x][y].Background)
		}

		if c.buffer[x][y].Char == 0 {
			continue
		}

		op := c.Font.ToOptions(c.buffer[x][y].Char)
		op.GeoM.Translate(0, float64(y*c.Font.TileHeight))

		if !c.Font.IsTile(c.buffer[x][y].Char) {
			op.ColorM.Scale(c.buffer[x][y].Foreground.Floats())
		}

		c.lines[x].DrawImage(c.Font.Image, op)
	}
}

func (c *Console) flushUpdates() {
	for i := range c.updates {
		c.updateLine(c.updates[i])
	}

	if len(c.updates) > 0 {
		c.updates = make([]int, 0)
	}
}

func (c *Console) draw(screen *ebiten.Image, offsetX, offsetY int) {
	c.flushUpdates()
	for x := range c.buffer {
		op := &ebiten.DrawImageOptions{}
		op.GeoM.Translate(float64((x+c.x+offsetX)*c.Font.TileWidth), float64((c.y+offsetY)*c.Font.TileHeight))
		screen.DrawImage(c.lines[x], op)
	}

	for i := range c.SubConsoles {
		c.SubConsoles[i].draw(screen, offsetX+c.x, offsetY+c.x)
	}
}

func (c *Console) update(screen *ebiten.Image) error {
	if ebiten.IsDrawingSkipped() {
		return nil
	}

	defer func() {
		c.lastFrame = time.Now().UnixNano()
	}()

	timeElapsed := float64((time.Now().UnixNano()-c.lastFrame)/(int64(time.Millisecond)/int64(time.Nanosecond))) / 1000.0

	if c.preRenderHook != nil {
		if err := c.preRenderHook(screen, timeElapsed); err != nil {
			return err
		}
	}

	c.mtx.RLock()
	c.draw(screen, 0, 0)
	c.mtx.RUnlock()

	if c.postRenderHook != nil {
		if err := c.postRenderHook(screen, timeElapsed); err != nil {
			return err
		}
	}

	if c.ShowFPS {
		ebitenutil.DebugPrint(screen, fmt.Sprintf("FPS: %0.2f", ebiten.CurrentFPS()))
	}

	return nil
}