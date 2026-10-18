// Package domain — модель меню и чистая логика проверки позиций (без I/O).
// Деньги — int64 копейки (ADR-13), float не используется.
package domain

// Money — сумма в минорных единицах.
type Money struct {
	Amount   int64
	Currency string
}

// Modifier — опция позиции.
type Modifier struct {
	ID        string
	Name      string
	Price     int64
	Available bool
}

// Item — позиция меню.
type Item struct {
	ID          string
	Name        string
	Description string
	Price       int64
	Available   bool
	Modifiers   []Modifier
}

// Category — категория меню.
type Category struct {
	ID    string
	Name  string
	Items []Item
}

// Menu — меню ресторана.
type Menu struct {
	RestaurantID string
	Version      int64
	Currency     string
	Categories   []Category
}
