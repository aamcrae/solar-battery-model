# Solar and Battery model
This program uses historical CSV data to model scenarios for
Solar, no Solar, and Solar+Battery situations.

Example output is:
```
Days: 1198, years: 3.3
              | Total cost |  Cost PA  |  Import  |  Export  |
No solar      |  $16502.92 |  $5031.46 |    41039 |        0 |
Solar         |   $5010.26 |  $1527.54 |    21333 |    34573 |
Solar+battery |   $1769.42 |   $539.47 |     7795 |    19536 |
Total consumption: 41039kWh, battery charging 15038kWh, battery discharge 13538kWh
Between no-solar/solar per day: $9.59, per year: $3503.92
Between no-solar/solar+battery per day: $12.30, per year: $4491.99
Between solar/solar+battery per day: $2.71, per year: $988.08
```
