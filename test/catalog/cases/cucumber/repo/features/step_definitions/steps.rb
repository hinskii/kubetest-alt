Given('the number {int}') { |n| @n = n }
Then('it is even') { raise "#{@n} is odd" unless @n.even? }
