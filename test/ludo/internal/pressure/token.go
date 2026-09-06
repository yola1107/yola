package pressure

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const tokenPrefix = "pressure:"

type MoneyRange struct {
	Min float64
	Max float64
}

func Token(money MoneyRange) (string, error) {
	if err := money.validate(); err != nil {
		return "", err
	}
	return tokenPrefix + strconv.FormatFloat(money.Min, 'g', -1, 64) + ":" +
		strconv.FormatFloat(money.Max, 'g', -1, 64), nil
}

func ParseToken(token string) (MoneyRange, bool, error) {
	payload, pressure := strings.CutPrefix(token, tokenPrefix)
	if !pressure {
		return MoneyRange{}, false, nil
	}
	minimum, maximum, found := strings.Cut(payload, ":")
	if !found {
		return MoneyRange{}, true, errors.New("pressure token money range is missing")
	}
	money := MoneyRange{}
	var err error
	money.Min, err = strconv.ParseFloat(minimum, 64)
	if err != nil {
		return MoneyRange{}, true, errors.New("pressure token minimum money is invalid")
	}
	money.Max, err = strconv.ParseFloat(maximum, 64)
	if err != nil {
		return MoneyRange{}, true, errors.New("pressure token maximum money is invalid")
	}
	if err = money.validate(); err != nil {
		return MoneyRange{}, true, err
	}
	return money, true, nil
}

func (money MoneyRange) validate() error {
	if math.IsNaN(money.Min) || math.IsInf(money.Min, 0) || math.IsNaN(money.Max) || math.IsInf(money.Max, 0) {
		return errors.New("pressure money range must be finite")
	}
	if money.Min < 0 || money.Max < money.Min {
		return fmt.Errorf("pressure money range [%v,%v] is invalid", money.Min, money.Max)
	}
	return nil
}
