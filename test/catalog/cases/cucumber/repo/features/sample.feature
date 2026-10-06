Feature: kubetest catalog
  Scenario: an even number
    Given the number 2
    Then it is even

  Scenario: an odd number fails on purpose
    Given the number 3
    Then it is even
