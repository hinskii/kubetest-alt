def test_passes():
    assert 1 + 1 == 2


def test_fails_on_purpose():
    assert 1 + 1 == 3, "deliberate failure: the catalog e2e expects one red test"
