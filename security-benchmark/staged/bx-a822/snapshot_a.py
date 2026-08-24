def execute(data):
    credit = 0
    for _ in range(data["deliveries"]):
        credit += data["amount"]
    return {"credit": credit}
