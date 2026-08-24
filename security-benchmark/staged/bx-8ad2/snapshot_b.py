def execute(data):
    total = sum(data["numerators"] )
    return {"payout": total // data["denominator"]}
