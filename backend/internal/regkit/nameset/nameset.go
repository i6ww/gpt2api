// Package nameset 注册流程中通用的随机姓名 / 密码 / 生日生成器。
package nameset

import (
	"crypto/rand"
	"math/big"
	"strings"
	"time"
)

// 常用美式名字（First Name）。注册需要"看起来像真人"，不要全 abc。
var firstNames = []string{
	"James", "Mary", "Robert", "Patricia", "John", "Jennifer", "Michael", "Linda",
	"William", "Elizabeth", "David", "Barbara", "Richard", "Susan", "Joseph", "Jessica",
	"Thomas", "Sarah", "Charles", "Karen", "Christopher", "Lisa", "Daniel", "Nancy",
	"Matthew", "Betty", "Anthony", "Helen", "Mark", "Sandra", "Donald", "Donna",
	"Steven", "Carol", "Paul", "Ruth", "Andrew", "Sharon", "Joshua", "Michelle",
	"Kenneth", "Laura", "Kevin", "Sarah", "Brian", "Kimberly", "George", "Deborah",
	"Edward", "Dorothy", "Ronald", "Amy", "Timothy", "Angela", "Jason", "Ashley",
	"Jeffrey", "Brenda", "Ryan", "Emma", "Jacob", "Olivia", "Gary", "Cynthia",
	"Nicholas", "Marie", "Eric", "Janet", "Jonathan", "Catherine", "Stephen", "Frances",
	"Larry", "Christine", "Justin", "Samantha", "Scott", "Debra", "Brandon", "Rachel",
}

// 常用美式姓（Last Name）。
var lastNames = []string{
	"Smith", "Johnson", "Williams", "Jones", "Brown", "Davis", "Miller", "Wilson",
	"Moore", "Taylor", "Anderson", "Thomas", "Jackson", "White", "Harris", "Martin",
	"Thompson", "Garcia", "Martinez", "Robinson", "Clark", "Rodriguez", "Lewis", "Lee",
	"Walker", "Hall", "Allen", "Young", "Hernandez", "King", "Wright", "Lopez",
	"Hill", "Scott", "Green", "Adams", "Baker", "Gonzalez", "Nelson", "Carter",
	"Mitchell", "Perez", "Roberts", "Turner", "Phillips", "Campbell", "Parker", "Evans",
	"Edwards", "Collins", "Stewart", "Sanchez", "Morris", "Rogers", "Reed", "Cook",
	"Morgan", "Bell", "Murphy", "Bailey", "Rivera", "Cooper", "Richardson", "Cox",
	"Howard", "Ward", "Torres", "Peterson", "Gray", "Ramirez", "James", "Watson",
	"Brooks", "Kelly", "Sanders", "Price", "Bennett", "Wood", "Barnes", "Ross",
}

func randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// Random 公开的安全随机整数（[0, n) 半开区间）。
func Random(n int) int { return randIntn(n) }

// FirstName 随机美式名字。
func FirstName() string { return firstNames[randIntn(len(firstNames))] }

// LastName 随机美式姓（约 5% 概率追加 " Jr"）。
func LastName() string {
	s := lastNames[randIntn(len(lastNames))]
	if randIntn(20) == 0 {
		s += " Jr"
	}
	return s
}

// Password 生成强密码（默认 16 位，必含大写、小写、数字、特殊符号各一）。
//
// length 不传或 <8 时回退到 16。
func Password(length int) string {
	if length < 8 {
		length = 16
	}
	const upper = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	const lower = "abcdefghjkmnpqrstuvwxyz"
	const digits = "23456789"
	const symbols = "!@#$%"
	const all = upper + lower + digits + symbols
	out := make([]byte, length)
	out[0] = upper[randIntn(len(upper))]
	out[1] = lower[randIntn(len(lower))]
	out[2] = digits[randIntn(len(digits))]
	out[3] = symbols[randIntn(len(symbols))]
	for i := 4; i < length; i++ {
		out[i] = all[randIntn(len(all))]
	}
	// Fisher-Yates 打乱。
	for i := length - 1; i > 0; i-- {
		j := randIntn(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// Birthday 生成 25-40 岁之间的合法生日（UTC）。
func Birthday() (year, month, day int) {
	currentYear := time.Now().UTC().Year()
	year = currentYear - 25 - randIntn(16) // 25..40 岁
	month = 1 + randIntn(12)
	maxDay := daysInMonth(year, month)
	day = 1 + randIntn(maxDay)
	return
}

// BirthdayString 返回 YYYY-MM-DD 字符串（部分接口要求）。
func BirthdayString() string {
	y, m, d := Birthday()
	var b strings.Builder
	b.Grow(10)
	b.WriteString(itoa(y, 4))
	b.WriteByte('-')
	b.WriteString(itoa(m, 2))
	b.WriteByte('-')
	b.WriteString(itoa(d, 2))
	return b.String()
}

func daysInMonth(year, month int) int {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	default:
		// 闰年判断
		leap := (year%4 == 0 && year%100 != 0) || year%400 == 0
		if leap {
			return 29
		}
		return 28
	}
}

func itoa(n, width int) string {
	if n < 0 {
		n = 0
	}
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		out[i] = byte('0' + n%10)
		n /= 10
	}
	return string(out)
}
