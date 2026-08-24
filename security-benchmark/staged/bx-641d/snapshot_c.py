def execute(data):
    remaining = data["balance"]
    paid = data["withdrawal"]
    if data["reenter"] and remaining >= data["withdrawal"]:
        paid += data["withdrawal"]
    return {"paid": paid}
